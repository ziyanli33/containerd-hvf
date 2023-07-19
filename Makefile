build:
	go build -o bin/containerd-shim-hvf-v1 ./cmd
	chmod +x bin/containerd-shim-hvf-v1
install:
	chmod +x bin/containerd-shim-hvf-v1 & mv bin/containerd-shim-hvf-v1 /usr/local/bin/containerd-shim-hvf-v1
services:
	LANG=en_US.UTF-8 sudo libvirtd &
	sudo virtlogd &
	sudo containerd &
image:
	rm img/cloudinit.iso 2> /dev/null
	hdiutil makehybrid -o img/cloudinit.iso -hfs -joliet -iso -default-volume-name cidata config/
	[ -f img/boot.qcow2 ] || wget -O img/boot.qcow2 https://cloud-images.ubuntu.com/releases/21.04/release/ubuntu-21.04-server-cloudimg-arm64.img
	docker build -t example.com/img/boot:latest .
	docker save -o img/boot.tar example.com/img/boot:latest
	sudo ctr image import img/boot.tar