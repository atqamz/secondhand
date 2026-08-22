{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = { nixpkgs, ... }:
    let
      version = "0.6.0"; # x-release-please-version
      # x86_64-darwin is excluded: the pinned nixpkgs-unstable aborts evaluation
      # for it since upstream dropped support, breaking every output.
      systems = [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      packages = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in {
          default = pkgs.buildGoModule {
            pname = "hand";
            inherit version;
            src = ./.;
            vendorHash = "sha256-J4BQ8DVzZaG+KMBa61tMDuoOtcH3AgcTYwr97mjLitU=";
            checkFlags = [ "-tags=test" ];
            ldflags = [ "-s" "-w" "-X main.version=v${version}" "-X main.channel=stable" "-X main.commit=" "-X main.distribution=nix" ];
            nativeCheckInputs = [ pkgs.git pkgs.gh ]; # test suite execs git and gh directly
            meta.mainProgram = "hand";
          };
        }
      );

      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in {
          default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.golangci-lint pkgs.gopls pkgs.gotools pkgs.gcc pkgs.jq ];
          };
        }
      );
    };
}
