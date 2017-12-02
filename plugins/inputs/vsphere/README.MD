# VMware vSphere Input Plugin

The VMware vSphere plugin uses the vSphere API to gather metrics from multiple vCenter servers.
 
* Clusters
* Hosts
* VMs
* Data stores

## Configuration

```
# Read metrics from one or many vCenters
[[inputs.vsphere]]
  ## List of vCenter URLs, including credentials.
  ## <protocol>://<user>:<password>@<vcenter>/sdk
  ## Note the "@" characted must be escaped as %40
  ## e.g.
  ##     https://administrator%40vsphere.local:password@vcenter.local/sdk
  #
  vcenters = [ "https://administrator%40vsphere.local:password@vcenter.local/sdk" ]
  
  ## gather metrics from Clusters (default: true)
  # gather_clusters = true
  # cluster_metrics = []
  
  ## gather metrics from Hosts (default: true)
  # gather_hosts = true
  # host_metrics = []
  
  ## gather metrics from VMs (default: true)
  # gather_vms = true
  # vm_metrics = []
  
  ## gather metrics from Data stores (default: true)
  # gather_datastores = true
  # datastore_metrics = []
  
  ## number of objects to retreive per query (default: 500)
  # objects_per_query = 500
  
  ## the interval before (re)discovering objects subject to metrics collection (default: 300s)
  # object_discovery_interval = "300s"
  
  ## timeout applies to any of the connection request made to vcenter
  # timeout = "20s"
```

### Metrics Selection

vSphere metrics are available in 4 categories: Clusters, Hosts, VMs, and Datastores. Each category can be enabled
independently, and for each category a list with wildcards can be applied to filter metrics. If the <category>_metrics 
property is not specified all metrics will be included for the category. Items can be specified with standard glob
style wildcards (*, ?). Any item can be negated with an exclamation mark (!) as the first character. Metrics will be
checked against every item in the list that applies, meaning you can include multiple metrics with the first item, and
negate selectively within that list on a subsequent item.

A configuration to include all metrics under the host category that start with `cpu.*` but will not include
`cpu.idle.summation`.

```
  host_metrics = ["cpu.*", "!cpu.idle.summation"]

``` 

## Measurements &amp; Fields

- Cluster Stats
	- Cluster services: CPU, memory, failover
	- CPU: total, usage
	- Memory: consumed, total, vmmemctl
	- VM operations: # changes, clone, create, deploy, destroy, power, reboot, reconfigure, register, reset, shutdown, standby, vmotion
- Host Stats:
	- CPU: total, usage, cost, mhz
	- Datastore: iops, latency, read/write bytes, # reads/writes
	- Disk: commands, latency, kernel reads/writes, # reads/writes, queues
	- Memory: total, usage, active, latency, swap, shared, vmmemctl
	- Network: broadcast, bytes, dropped, errors, multicast, packets, usage
	- Power: energy, usage, capacity
	- Res CPU: active, max, running
	- Storage Adapter: commands, latency, # reads/writes
	- Storage Path: commands, latency, # reads/writes
	- System Resources: cpu active, cpu max, cpu running, cpu usage, mem allocated, mem consumed, mem shared, swap
	- System: uptime
	- Flash Module: active VMDKs 
- VM Stats:
	- CPU: demand, usage, readiness, cost, mhz
	- Datastore: latency, # reads/writes
	- Disk: commands, latency, # reads/writes, provisioned, usage
	- Memory: granted, usage, active, swap, vmmemctl
	- Network: broadcast, bytes, dropped, multicast, packets, usage
	- Power: energy, usage
	- Res CPU: active, max, running
	- System: operating system uptime, uptime
	- Virtual Disk: seeks, # reads/writes, latency, load 
- Datastore stats:
	- Disk: Capacity, provisioned, used  
	
## Tags
- cpu stats for Host and VM
	- cpu (cpu core - not all CPU fields will have this tag)
- datastore stats for Host and VM
	- datastore (id of datastore)
- disk stats for Host and VM
	- disk (name of disk)
- disk.used.capacity for Datastore
	- disk (type of disk)
- net stats for Host and VM
	- interface (name of network interface)
- storageAdapter stats for Host
	- adapter (name of storage adapter)
- storagePath stats for Host 
	- path (id of storage path)
- sys.resource* stats for Host
	- resource (resource type)
- vflashModule stats for Host
	- module (name of flash module)
- virtualDisk stats for VM
	- disk (name of virtual disk)