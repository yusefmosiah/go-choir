{
  description = "go-choir: Distributed Multiagent Operating System";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs, ... }:
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
        vendorHash = "sha256-2Rg6bOMu4Ypi7C0NmwmG1Gv2h1/2oTn4z75yTwS3B6Q=";
        doCheck = false; # Tests run separately in CI
      };

      # Frontend package — built Svelte SPA via buildNpmPackage.
      # Local development uses pnpm (pnpm-lock.yaml); the Nix build uses npm
      # with a checked-in package-lock.json for reproducibility in the sandbox.
      # npmDepsHash will be updated on first Nix build on Node B (the error
      # message provides the correct hash, just like Go's vendorHash).
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
        npmDepsHash = "";
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
    {
      packages.${system} = goChoirPackages // {
        default = self.packages.${system}.auth;
      };

      nixosConfigurations.go-choir-b = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = {
          goChoirPackages = goChoirPackages;
        };
        modules = [
          ./nix/hardware.nix
          ./nix/disks.nix
          ./nix/node-b.nix
        ];
      };
    };
}
