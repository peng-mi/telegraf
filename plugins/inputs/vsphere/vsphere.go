package vsphere

import (
	"context"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/vmware/govmomi/performance"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"
)

type objectMap map[string]objectRef

type Endpoint struct {
	Parent       *VSphere
	Url          *url.URL
	intervals    []int32
	lastColl     map[string]time.Time
	hostMap      objectMap
	vmMap        objectMap
	clusterMap   objectMap
	datastoreMap objectMap
	nameCache    map[string]string
	bgObjectDisc bool
	collectMux   sync.RWMutex
}

type VSphere struct {
	Vcenters                []string
	VmSamplingPeriod        internal.Duration
	HostSamplingPeriod      internal.Duration
	ClusterSamplingPeriod   internal.Duration
	DatastoreSamplingPeriod internal.Duration
	ObjectDiscoveryInterval internal.Duration
	ObjectsPerQuery         int32
	endpoints               []Endpoint
}

type objectRef struct {
	name      string
	ref       types.ManagedObjectReference
	parentRef *types.ManagedObjectReference //Pointer because it must be nillable
}

type InstanceMetrics map[string]map[string]interface{}

func NewEndpoint(parent *VSphere, url *url.URL) Endpoint {
	hostMap := make(objectMap)
	vmMap := make(objectMap)
	clusterMap := make(objectMap)
	datastoreMap := make(objectMap)
	e := Endpoint{
		Url:          url,
		Parent:       parent,
		lastColl:     make(map[string]time.Time),
		hostMap:      hostMap,
		vmMap:        vmMap,
		clusterMap:   clusterMap,
		datastoreMap: datastoreMap,
		nameCache:    make(map[string]string),
	}
	e.init()
	return e
}

func (e *Endpoint) init() error {
	conn, err := NewConnection(e.Url)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Load interval table
	//
	ctx := context.Background()
	list, err := conn.Perf.HistoricalInterval(ctx)
	if err != nil {
		return err
	}
	e.intervals = make([]int32, len(list))
	for k, i := range list {
		e.intervals[k] = i.SamplingPeriod
	}
	return nil
}

func (e *Endpoint) discover() error {
	conn, err := NewConnection(e.Url)
	if err != nil {
		return err
	}

	defer conn.Close()

	nameCache := make(map[string]string)

	// Discover clusters
	//
	ctx := context.Background()
	clusterMap, err := e.getClusters(ctx, conn.Root)
	if err != nil {
		return err
	}
	for _, cluster := range clusterMap {
		nameCache[cluster.ref.Reference().Value] = cluster.name
	}

	// Discover hosts
	//
	hostMap, err := e.getHosts(ctx, conn.Root)
	if err != nil {
		return err
	}
	for _, host := range hostMap {
		nameCache[host.ref.Reference().Value] = host.name
	}

	// Discover VMs
	//
	vmMap, err := e.getVMs(ctx, conn.Root)
	if err != nil {
		return err
	}
	for _, vm := range vmMap {
		nameCache[vm.ref.Reference().Value] = vm.name
	}

	datastoreMap, err := e.getDatastores(ctx, conn.Root)
	if err != nil {
		return err
	}
	for _, datastore := range datastoreMap {
		nameCache[datastore.ref.Reference().Value] = datastore.name
	}

	// Atomically swap maps
	//
	e.collectMux.Lock()
	defer e.collectMux.Unlock()

	e.nameCache = nameCache
	e.datastoreMap = datastoreMap
	e.vmMap = vmMap
	e.hostMap = hostMap
	e.clusterMap = clusterMap
	return nil
}

func (e *Endpoint) collectResourceType(p *performance.Manager, ctx context.Context, alias string, acc telegraf.Accumulator,
	objects objectMap, nameCache map[string]string, intervalDuration internal.Duration, isRealTime bool) error {

	// Object maps may change, so we need to hold the collect lock
	//
	e.collectMux.RLock()
	defer e.collectMux.RUnlock()

	// Interval = 0 means collection for this metric was diabled, so don't even bother.
	//
	interval := int32(intervalDuration.Duration.Seconds())
	if interval <= 0 {
		return nil
	}
	log.Printf("D! Resource type: %s, sampling interval is: %d", alias, interval)

	// Do we have new data yet?
	//
	now := time.Now()
	latest, hasLatest := e.lastColl[alias]
	if hasLatest {
		elapsed := time.Now().Sub(latest).Seconds()
		if elapsed < float64(interval) {
			// No new data would be available. We're outta here!
			//
			return nil
		}
	}
	e.lastColl[alias] = now
	log.Printf("D! Collecting for %s", alias)
	fullAlias := "vsphere." + alias

	start := time.Now()
	log.Printf("D! Query for %s returned %d objects", alias, len(objects))
	pqs := make([]types.PerfQuerySpec, 0, e.Parent.ObjectsPerQuery)
	total := 0
	for _, object := range objects {
		pq := types.PerfQuerySpec{
			Entity:     object.ref,
			MaxSample:  1,
			MetricId:   nil,
			IntervalId: interval,
		}

		if !isRealTime {
			startTime := now.Add(-time.Duration(interval) * time.Second)
			pq.StartTime = &startTime
			pq.EndTime = &now
		}

		pqs = append(pqs, pq)
		total++

		// Filled up a chunk or at end of data? Run a query with the collected objects
		//
		if len(pqs) >= int(e.Parent.ObjectsPerQuery) || total == len(objects) {
			log.Printf("D! Querying %d objects of type %s for %s. Total processed: %d. Total objects %d\n", len(pqs), alias, e.Url.Host, total, len(objects))
			metrics, err := p.Query(ctx, pqs)
			if err != nil {
				log.Printf("E! Error processing resource type %s", alias)
				return err
			}

			ems, err := p.ToMetricSeries(ctx, metrics)
			if err != nil {
				return err
			}

			// Iterate through result and fields list
			//
			for _, em := range ems {
				moid := em.Entity.Reference().Value
				for _, v := range em.Value {
					name := v.Name
					for idx, value := range v.Value {
						f := map[string]interface{}{name: value}
						objectName := nameCache[moid]
						parent := ""
						parentRef := objects[moid].parentRef
						if parentRef != nil {
							parent = nameCache[parentRef.Value]
						}

						t := map[string]string{
							"vcenter":  e.Url.Host,
							"hostname": objectName,
							"moid":     moid,
							"parent":   parent}

						if v.Instance != "" {
							if strings.HasPrefix(name, "cpu.") {
								t["cpu"] = v.Instance
							} else if strings.HasPrefix(name, "net.") {
								t["interface"] = v.Instance
							} else if strings.HasPrefix(name, "sys.resource") {
								t["resource"] = v.Instance
							} else if strings.HasPrefix(name, "disk.") || strings.HasPrefix(name, "virtualDisk.") {
								t["disk"] = v.Instance
							} else if strings.HasPrefix(name, "datastore.") {
								t["datastore"] = v.Instance
							} else if strings.HasPrefix(name, "storagePath.") {
								t["path"] = v.Instance
							} else if strings.HasPrefix(name, "storageAdapter.") {
								t["adapter"] = v.Instance
							} else if strings.HasPrefix(name, "vflashModule.") {
								t["module"] = v.Instance
							} else {
								// default to instance
								t["instance"] = v.Instance
							}
						}
						acc.AddFields(fullAlias, f, t, em.SampleInfo[idx].Timestamp)
					}
				}
			}
			pqs = make([]types.PerfQuerySpec, 0, e.Parent.ObjectsPerQuery)
		}
	}

	log.Printf("D! Collection of %s took %v\n", alias, time.Now().Sub(start))
	return nil
}

func (e *Endpoint) collect(acc telegraf.Accumulator) error {
	err := e.discover()
	if err != nil {
		return err
	}

	conn, err := NewConnection(e.Url)
	if err != nil {
		return err
	}

	defer conn.Close()

	ctx := context.Background()
	err = e.collectResourceType(conn.Perf, ctx, "cluster", acc, e.clusterMap, e.nameCache, e.Parent.ClusterSamplingPeriod, false)
	if err != nil {
		return err
	}

	err = e.collectResourceType(conn.Perf, ctx, "host", acc, e.hostMap, e.nameCache, e.Parent.HostSamplingPeriod, true)
	if err != nil {
		return err
	}

	err = e.collectResourceType(conn.Perf, ctx, "vm", acc, e.vmMap, e.nameCache, e.Parent.VmSamplingPeriod, true)
	if err != nil {
		return err
	}

	err = e.collectResourceType(conn.Perf, ctx, "datastore", acc, e.datastoreMap, e.nameCache, e.Parent.DatastoreSamplingPeriod, false)
	if err != nil {
		return err
	}

	return nil
}

func (e *Endpoint) getVMs(ctx context.Context, root *view.ContainerView) (objectMap, error) {
	var resources []mo.VirtualMachine
	err := root.Retrieve(ctx, []string{"VirtualMachine"}, []string{"summary", "runtime.host"}, &resources)
	if err != nil {
		return nil, err
	}
	m := make(objectMap)
	for _, r := range resources {
		m[r.ExtensibleManagedObject.Reference().Value] = objectRef{
			name: r.Summary.Config.Name, ref: r.ExtensibleManagedObject.Reference(), parentRef: r.Runtime.Host}
	}
	return m, nil
}

func (e *Endpoint) getHosts(ctx context.Context, root *view.ContainerView) (objectMap, error) {
	var resources []mo.HostSystem
	err := root.Retrieve(ctx, []string{"HostSystem"}, []string{"summary", "parent"}, &resources)
	if err != nil {
		return nil, err
	}
	m := make(objectMap)
	for _, r := range resources {
		m[r.ExtensibleManagedObject.Reference().Value] = objectRef{
			name: r.Summary.Config.Name, ref: r.ExtensibleManagedObject.Reference(), parentRef: r.Parent}
	}
	return m, nil
}

func (e *Endpoint) getClusters(ctx context.Context, root *view.ContainerView) (objectMap, error) {
	var resources []mo.ClusterComputeResource
	err := root.Retrieve(ctx, []string{"ClusterComputeResource"}, []string{"summary", "name", "parent"}, &resources)
	if err != nil {
		return nil, err
	}
	m := make(objectMap)
	for _, r := range resources {
		m[r.ExtensibleManagedObject.Reference().Value] = objectRef{
			name: r.Name, ref: r.ExtensibleManagedObject.Reference(), parentRef: r.Parent}
	}
	return m, nil
}

func (e *Endpoint) getDatastores(ctx context.Context, root *view.ContainerView) (objectMap, error) {
	var resources []mo.Datastore
	err := root.Retrieve(ctx, []string{"Datastore"}, []string{"summary"}, &resources)
	if err != nil {
		return nil, err
	}
	m := make(objectMap)
	for _, r := range resources {
		m[r.Summary.Name] = objectRef{ref: r.ExtensibleManagedObject.Reference(), parentRef: r.Parent}
	}
	return m, nil
}

var sampleConfig = `
  ## List of vCenter URLs, including credentials. Note the "@" characted must be escaped as %40
  # vcenters = [ "https://administrator%40vsphere.local:VMware1!@vcenter.local/sdk" ]
`

func (v *VSphere) SampleConfig() string {
	return sampleConfig
}

func (v *VSphere) Description() string {
	return "Read metrics from VMware vCenter"
}

func (v *VSphere) vSphereInit() {
	if v.endpoints != nil {
		return
	}

	v.endpoints = make([]Endpoint, len(v.Vcenters))
	for i, rawUrl := range v.Vcenters {
		u, err := soap.ParseURL(rawUrl)
		if err != nil {
			log.Printf("E! Can't parse URL %s\n", rawUrl)
		}
		v.endpoints[i] = NewEndpoint(v, u)
	}
}

func (v *VSphere) Gather(acc telegraf.Accumulator) error {

	v.vSphereInit()

	start := time.Now()

	var wg sync.WaitGroup

	for _, ep := range v.endpoints {
		wg.Add(1)
		go func(target Endpoint) {
			defer wg.Done()
			acc.AddError(target.collect(acc))
		}(ep)
	}

	wg.Wait()

	// Add another counter to show how long it took to gather all the metrics on this cycle (can be used to tune # of vCenters and collection intervals per telegraf agent)
	acc.AddCounter("vsphere", map[string]interface{}{"gather.duration": time.Now().Sub(start).Seconds()}, nil, time.Now())

	return nil
}

func init() {
	inputs.Add("vsphere", func() telegraf.Input {
		return &VSphere{
			Vcenters:                []string{},
			VmSamplingPeriod:        internal.Duration{Duration: time.Second * 20},
			HostSamplingPeriod:      internal.Duration{Duration: time.Second * 20},
			ClusterSamplingPeriod:   internal.Duration{Duration: time.Second * 300},
			DatastoreSamplingPeriod: internal.Duration{Duration: time.Second * 300},
			ObjectDiscoveryInterval: internal.Duration{Duration: time.Second * 300},
			ObjectsPerQuery:         500,
		}
	})
}