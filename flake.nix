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
            (pkgs.lib.hasSuffix ".go" path) ||
            (pkgs.lib.hasSuffix "go.mod" path) ||
            (pkgs.lib.hasSuffix "go.sum" path) ||
            (baseNameOf path) == "go.mod" ||
            (baseNameOf path) == "go.sum";
        };
        vendorHash = null; # No external Go dependencies yet
        doCheck = false; # Tests run separately in CI
      };

      # Frontend package — build Svelte app with pnpm
      frontendPkg = pkgs.stdenv.mkDerivation {
        pname = "go-choir-frontend";
        version = goModuleVersion;
        src = pkgs.lib.cleanSource ./frontend;

        nativeBuildInputs = with pkgs; [
          nodejs
          pnpm
        ];

        # pnpm fetch — uses pnpm-lock.yaml for deterministic downloads
        postPatch = ''
          pnpm install --frozen-lockfile
        '';

        buildPhase = ''
          runHook preBuild
          pnpm build
          runHook postBuild
        '';

        installPhase = ''
          runHook preInstall
          mkdir -p $out
          cp -r dist/* $out/
          runHook postInstall
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
