{
  description = "Zonaprop bot - Go dev shell";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }: {
    devShells.x86_64-linux.default =
      let pkgs = import nixpkgs { system = "x86_64-linux"; };
      in pkgs.mkShell {
        packages = [ pkgs.go pkgs.gopls pkgs.gofumpt ];
      };
  };
}