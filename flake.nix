{
  description = "Herald - RSS feed aggregator";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go_1_24
            golangci-lint
            gotools
            gopls
            delve
          ];

          shellHook = ''
            echo "Herald development environment"
            echo "Go version: $(go version)"
            echo "golangci-lint version: $(golangci-lint version)"
          '';
        };
      }
    );
}
