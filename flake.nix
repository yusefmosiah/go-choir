{
  description = "go-choir: Distributed Multiagent Operating System";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # Upstream microvm.nix for building NixOS guest VM images.
    # Used to generate the Firecracker-compatible kernel, initrd, rootfs,
    # and erofs store disk for sandbox VMs. The Go control plane
    # (vmmanager/vmctl) launches Firecracker with these artifacts.
    # Not using the fork — upstream is stable and well-maintained.
    microvm = {
      url = "github:microvm-nix/microvm.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, microvm, ... }:
    let
      # Packages are x86_64-linux only (deployment target)
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };

      # Go module version from go.mod
      goModuleVersion = "0.1.0";

      # Common buildGoModule args for all Go services
      commonGoArgs = {
        src = pkgs.lib.cleanSourceWith {
          src = ./.;
          filter = path: type:
            type == "directory" ||
            (pkgs.lib.hasSuffix ".go" path) ||
            (baseNameOf path == "go.mod") ||
            (baseNameOf path == "go.sum");
        };
        vendorHash = "";
        doCheck = false; # Tests run separately in CI
      };

      # Frontend package — built Svelte SPA via buildNpmPackage.
      # Local development uses pnpm (pnpm-lock.yaml); the Nix build uses npm
      # with a checked-in package-lock.json for reproducibility in the sandbox.
      # npmDepsHash was computed with `nix run nixpkgs#prefetch-npm-deps --
      # frontend/package-lock.json`. If dependencies change, re-run the
      # prefetch command (or set npmDepsHash to "" and read the correct hash
      # from the first Nix build error, just like Go's vendorHash).
      frontendPkg = pkgs.buildNpmPackage {
        pname = "go-choir-frontend";
        version = goModuleVersion;
        src = pkgs.lib.cleanSourceWith {
          src = ./frontend;
          filter = path: type:
            let
              base = baseNameOf path;
            in
            if type == "directory" then
              base != "node_modules" && base != "test-results" && base != ".cache"
            else
              (pkgs.lib.hasSuffix ".js" path) ||
              (pkgs.lib.hasSuffix ".svelte" path) ||
              (pkgs.lib.hasSuffix ".css" path) ||
              (pkgs.lib.hasSuffix ".html" path) ||
              base == "package.json" ||
              base == "package-lock.json" ||
              base == "svelte.config.js" ||
              base == "vite.config.js";
        };
        npmDepsHash = "sha256-ZZNGgjuxa7b6sVuREh9v8znFYLu0AChAaf95dfxtNHg=";
        npmBuildScript = "build";
        # Playwright downloads browsers during postinstall, which fails in the
        # Nix sandbox.  We only need it for e2e tests (not the build), so skip.
        PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
        installPhase = ''
          cp -r dist $out
        '';
      };

      # Build a single Go service binary
      mkGoService = { pname, subPackage }:
        pkgs.buildGoModule (commonGoArgs // {
          inherit pname;
          version = goModuleVersion;
          subPackages = [ subPackage ];
        });

      # All packages
      goChoirPackages = {
        auth = mkGoService {
          pname = "auth";
          subPackage = "cmd/auth";
        };
        proxy = mkGoService {
          pname = "proxy";
          subPackage = "cmd/proxy";
        };
        vmctl = mkGoService {
          pname = "vmctl";
          subPackage = "cmd/vmctl";
        };
        gateway = mkGoService {
          pname = "gateway";
          subPackage = "cmd/gateway";
        };
        sandbox = mkGoService {
          pname = "sandbox";
          subPackage = "cmd/sandbox";
        };
        frontend = frontendPkg;
      };

    in
    let
      # ── Guest VM artifacts ──────────────────────────────────────────────
      # The sandbox guest VM is defined as a NixOS configuration using
      # microvm.nix. From it we extract the individual artifacts that
      # vmmanager needs to launch Firecracker:
      #   - vmlinux (kernel)
      #   - initrd (for systemd module loading)
      #   - store disk (erofs for the nix store closure)
      #
      # The guest-image package bundles these for easy deployment:
      #   nix build .#guest-image
      #   cp result/vmlinux result/initrd result/storedisk.erofs /var/lib/go-choir/guest/
      guestVmConfig = self.nixosConfigurations.go-choir-sandbox-vm.config;

      # Guest kernel (vmlinux ELF binary for Firecracker).
      guestKernel = guestVmConfig.boot.kernelPackages.kernel.dev;

      # Guest initrd (contains ext4, erofs, virtio modules needed by systemd).
      guestInitrd = guestVmConfig.system.build.initialRamdisk;

      # Guest store disk (erofs image containing the nix store closure).
      # This is the shared read-only nix store that all VMs reference.
      # With KSM on the host, identical pages are deduplicated across VMs.
      guestStoreDisk = guestVmConfig.microvm.storeDisk;

      # Convenience package that bundles all guest artifacts together.
      # The CI deploy script copies these to /var/lib/go-choir/guest/.
      guest-image = pkgs.runCommand "go-choir-guest-image" { } ''
        mkdir -p $out
        cp ${guestKernel}/vmlinux $out/vmlinux
        cp ${guestInitrd}/${guestVmConfig.system.boot.loader.initrdFile} $out/initrd
        cp ${guestStoreDisk} $out/storedisk.erofs
      '';
    in
    {
      packages.${system} = goChoirPackages // {
        default = self.packages.${system}.auth;
        # Expose the guest image as a top-level package for easy building:
        #   nix build .#guest-image
        inherit guest-image;
      };

      # ── Sandbox guest VM NixOS configuration ──────────────────────────
      # This defines the guest VM that runs inside Firecracker on Node B.
      # Uses upstream microvm.nix to build the guest kernel, initrd, rootfs,
      # and erofs store disk. The Go vmmanager launches Firecracker with
      # these artifacts — it does NOT use the microvm runner scripts directly
      # because vmmanager needs per-VM networking, port assignment, and
      # lifecycle control.
      #
      # Key design (aligned with choiros-rs proven approach):
      #   - systemd as init (proper NixOS boot, not custom init script)
      #   - erofs for shared nix store with KSM deduplication
      #   - virtio-blk for data volumes (mutable sandbox state)
      #   - No virtiofs/9p shares (simpler, no host daemon needed)
      nixosConfigurations.go-choir-sandbox-vm = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = {
          goChoirPackages = goChoirPackages;
        };
        modules = [
          microvm.nixosModules.microvm
          ./nix/sandbox-vm.nix
        ];
      };

      # ── Node B host configuration ─────────────────────────────────────
      nixosConfigurations.go-choir-b = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = {
          goChoirPackages = goChoirPackages;
          # Pass the guest VM runner artifacts to the host config so
          # the deploy pipeline can install them to /var/lib/go-choir/guest/.
          guestRunner = self.nixosConfigurations.go-choir-sandbox-vm.config.microvm.runner.firecracker;
        };
        modules = [
          ./nix/hardware.nix
          ./nix/disks.nix
          ./nix/node-b.nix
        ];
      };
    };
}
