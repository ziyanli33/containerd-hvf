package hvf

import (
	"path/filepath"

	"github.com/google/uuid"
	"libvirt.org/go/libvirtxml"
)

func RenderDomain(id, bundle string) *libvirtxml.Domain {
	dom := libvirtxml.Domain{
		// This type is required to use macOS hypervisor framework
		Type: "hvf",
		Name: id,
		UUID: uuid.New().String(),
		Memory: &libvirtxml.DomainMemory{
			Value: 2,
			Unit:  "GiB",
		},
		CurrentMemory: &libvirtxml.DomainCurrentMemory{
			Value: 2,
			Unit:  "GiB",
		},
		CPU: &libvirtxml.DomainCPU{
			Mode:  "custom",
			Match: "exact",
			Model: &libvirtxml.DomainCPUModel{Value: "host"},
		},
		VCPU: &libvirtxml.DomainVCPU{
			Value: 8,
		},
		OS: &libvirtxml.DomainOS{
			Firmware: "efi", // BIOS not supported for aarch64
			Type: &libvirtxml.DomainOSType{
				Arch:    "aarch64",
				Machine: "virt",
				Type:    "hvm",
			},
			BootDevices: []libvirtxml.DomainBootDevice{
				{Dev: "hd"},
			},
		},
		Features: &libvirtxml.DomainFeatureList{
			ACPI: &libvirtxml.DomainFeature{},
			APIC: &libvirtxml.DomainFeatureAPIC{},
		},
		Clock: &libvirtxml.DomainClock{
			Offset: "localtime",
		},
		OnPoweroff: "destroy",
		OnReboot:   "restart",
		OnCrash:    "restart",
		Devices: &libvirtxml.DomainDeviceList{
			Emulator: "/opt/homebrew/bin/qemu-system-aarch64",
			Controllers: []libvirtxml.DomainController{
				{
					Type:  "usb",
					Model: "qemu-xhci", // USB 3.0
				},
			},
			Disks: []libvirtxml.DomainDisk{
				{
					Device: "cdrom",
					Driver: &libvirtxml.DomainDiskDriver{
						Name:  "qemu",
						Type:  "raw",
						Cache: "none",
					},
					Source: &libvirtxml.DomainDiskSource{
						File: &libvirtxml.DomainDiskSourceFile{
							File: filepath.Join(bundle, "rootfs", defaultRootImagePath, defaultCloudInitImageFileName),
						},
					},
					Target:   &libvirtxml.DomainDiskTarget{Dev: "vda", Bus: "sata"},
					ReadOnly: &libvirtxml.DomainDiskReadOnly{},
					Address: &libvirtxml.DomainAddress{
						Drive: &libvirtxml.DomainAddressDrive{},
					},
				},
				{
					Device: "disk",
					Driver: &libvirtxml.DomainDiskDriver{
						Name: "qemu",
						Type: "qcow2",
					},
					Source: &libvirtxml.DomainDiskSource{
						File: &libvirtxml.DomainDiskSourceFile{
							File: filepath.Join(bundle, "rootfs", defaultRootImagePath, defaultRootImageFileName),
						},
					},
					Target: &libvirtxml.DomainDiskTarget{Dev: "vdb", Bus: "virtio"},
				},
			},
			Inputs: []libvirtxml.DomainInput{
				{
					Type: "tablet",
					Bus:  "usb", // PS2 not supported
				},
				{
					Type: "keyboard",
					Bus:  "usb",
				},
			},
			// Refer to https://libvirt.org/formatdomain.html#relationship-between-serial-ports-and-consoles
			Consoles: []libvirtxml.DomainConsole{
				{
					Target: &libvirtxml.DomainConsoleTarget{
						Type: "serial",
					},
					Source: &libvirtxml.DomainChardevSource{
						Pty: &libvirtxml.DomainChardevSourcePty{},
					},
				},
			},
			Graphics: []libvirtxml.DomainGraphic{
				{
					VNC: &libvirtxml.DomainGraphicVNC{
						AutoPort: "yes",
						Listen:   "0.0.0.0",
						Listeners: []libvirtxml.DomainGraphicListener{
							{
								Address: &libvirtxml.DomainGraphicListenerAddress{
									Address: "0.0.0.0",
								},
							},
						},
					},
				},
			},
			Videos: []libvirtxml.DomainVideo{
				{
					Model: libvirtxml.DomainVideoModel{
						Type:    "virtio",
						Heads:   1,
						Primary: "yes",
					},
				},
			},
			MemBalloon: &libvirtxml.DomainMemBalloon{
				Model: "virtio",
			},
		},
		// Automatic tap interface setup is not supported on macOS,
		// use vmNet API from HVF instead.
		QEMUCommandline: &libvirtxml.DomainQEMUCommandline{
			Args: []libvirtxml.DomainQEMUCommandlineArg{
				{Value: "-netdev"},
				{Value: "vmnet-shared,id=net0"},
				{Value: "-device"},
				{Value: "virtio-net-device,netdev=net0"},
			},
		},
	}
	return &dom
}
