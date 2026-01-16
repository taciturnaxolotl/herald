{
  description = "Herald - RSS feed aggregator";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      allSystems = [
        "x86_64-linux" # 64-bit Intel/AMD Linux
        "aarch64-linux" # 64-bit ARM Linux
        "x86_64-darwin" # 64-bit Intel macOS
        "aarch64-darwin" # 64-bit ARM macOS
      ];
      forAllSystems =
        f:
        nixpkgs.lib.genAttrs allSystems (
          system:
          f {
            pkgs = import nixpkgs { inherit system; };
          }
        );
    in
    {
      packages = forAllSystems (
        { pkgs }:
        {
          default = pkgs.buildGoModule {
            pname = "herald";
            version = "0.1.1";
            subPackages = [ "." ];
            src = self;
            vendorHash = "sha256-SjxTy/ecSUYaJJ8dpfQFLF7WgVEpnKcu5qWcqyw611Q=";
            proxyVendor = true;
            ldflags = [
              "-X main.commitHash=${self.rev or self.dirtyRev or "dev"}"
              "-X main.version=0.1.1"
            ];
          };
        }
      );

      devShells = forAllSystems (
        { pkgs }:
        {
          default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go_1_24
              gopls
              gotools
              go-tools
              golangci-lint
              delve
              goreleaser
            ];

            shellHook = ''
              echo "Herald development environment"
              echo "Go version: $(go version)"
              echo "golangci-lint version: $(golangci-lint version)"
            '';
          };
        }
      );

      apps = forAllSystems (
        { pkgs }:
        {
          default = {
            type = "app";
            program = "${self.packages.${pkgs.system}.default}/bin/herald";
          };
        }
      );
    };
}
