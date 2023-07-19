# containerd-hvf

## Introduction
containerd-hvf is an experimental [containerd](https://containerd.io) [shim](https://github.com/containerd/containerd/tree/main/runtime/v2) for running VMs on macOS with the benefits of [libvirt](https://libvirt.org/) and the acceleration of [Hypervisor Framework](https://developer.apple.com/documentation/hypervisor).

> WARNING: This project is for development and study purpose only.
Do not use in production.

containerd-hvf doesn't offer the usual level of VM features that is achievable on other platforms due to limited use cases & macOS kernel API.

What containerd-hvf provides is:

* Start a VM in a container way
* Once container is started, the VM is ready for use, with ethernet connection. No initialization for OS needed.

## Prerequisites

* MacOS With Apple Silicon(M series chip)
* Install `golang` and `docker`

## Usage
### Preparation
Install `qemu` & `libvirt`
```
brew install qemu libvirt
```
Download containerd & build
```
go get github.com/containerd/containerd@v1.7.2
cd /pkg/mod/github.com/containerd/containerd@v1.7.2
make
sudo make install
```
Download containerd-hvf & build
```
# In this project root directory
make
sudo make install
```
### Run
Run `libvirtd`, `virtlogd` & `containerd`
```
sudo make services
```
Build local VM image
```
sudo make image
```
Run your first macOS container VM
```
sudo ctr run --privileged --d --runtime "io.containerd.hvf.v1" example.com/img/boot:latest samplevm
```
Enter your VM
```
virsh console samplevm
# username: hvf
# password: linux
# See config/user-data
```

### Debug
To stop a container
```
sudo ctr task kill samplevm
sudo ctr container rm samplevm
# The VM will be automatically removed.
```
In case the container is not cleaned up
```
rm -rf /var/run/containerd/io.containerd.runtime.v2.task/default/samplevm
sudo ctr snapshot rm samplevm
```

Shim work directory `/var/run/containerd/io.containerd.runtime.v2.task/default/:id`

Shim log directory `/var/log/containerd-shim-hvf-v1/:id/shim.log`

Libvirt log directory `/opt/homebrew/var/log/libvirt/qemu/:id.log`

## References
1. [Kubevirt](https://kubevirt.io/)
2. [Kata](https://katacontainers.io/)
3. [llma-vm](https://github.com/lima-vm/lima)