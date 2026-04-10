# Hardware configuration for OVH SYS-1 bare metal (Node B)
# Intel Xeon-E 2136 / 32GB RAM / 2x NVMe RAID1 / UEFI
# Adapted from choiros-rs nix/hosts/ovh-node-hardware.nix
{ config, lib, pkgs, modulesPath, ... }:
{
  imports = [ (modulesPath + "/installer/scan/not-detected.nix") ];

  boot.initrd.availableKernelModules = [
    "ahci"
    "nvme"
    "sd_mod"
    "xhci_pci"
    "usb_storage"
    "usbhid"
    "raid1"
    "md_mod"
    "btrfs"
  ];
  boot.kernelModules = [ "kvm-intel" ];
  boot.swraid.enable = true;
  boot.swraid.mdadmConf = "MAILADDR root";

  nixpkgs.hostPlatform = lib.mkDefault "x86_64-linux";
  hardware.cpu.intel.updateMicrocode = lib.mkDefault config.hardware.enableRedistributableFirmware;
}
