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

      # Frontend package — placeholder index.html for Caddy to serve.
      # The real Svelte build pipeline with pnpm will be added in Mission 2
      # when the frontend has real content. For now, Nix just needs to produce
      # an index.html with "go-choir" for Caddy's file_server.
      frontendPkg = pkgs.runCommand "go-choir-frontend" {
        pname = "go-choir-frontend";
        version = goModuleVersion;
      } ''
        mkdir -p $out
        cat > $out/index.html <<'EOF'
        <!DOCTYPE html>
        <html lang="en">
          <head>
            <meta charset="UTF-8" />
            <meta name="viewport" content="width=device-width, initial-scale=1.0" />
            <title>go-choir</title>
          </head>
          <body>
            <h1>go-choir</h1>
            <p>Distributed multiagent operating system</p>
          </body>
        </html>
        EOF
      '';

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
