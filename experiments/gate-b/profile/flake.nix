{
  description = "Boxface Gate B user package profile";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/7c8764b7c7b09b34f632464276218ef9090eaa11";
    home-manager = {
      url = "github:nix-community/home-manager/db7d5e2332710f5abb088f6b5de927d7f9511b35";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { nixpkgs, home-manager, ... }:
    let
      pkgs = import nixpkgs {
        system = "x86_64-linux";
        config.allowUnfreePredicate = pkg: builtins.elem (nixpkgs.lib.getName pkg) [ "claude-code" ];
      };
    in
    {
      homeConfigurations.dev = home-manager.lib.homeManagerConfiguration {
        inherit pkgs;
        modules = [
          {
            home.username = "dev";
            home.homeDirectory = "/home/dev";
            home.stateVersion = "26.05";
            home.packages = with pkgs; [
              claude-code
              codex
              opencode
              pi-coding-agent
              mise
              nodejs
              rustc
              cargo
              gcc
              git
              cacert
            ];
          }
        ];
      };
    };
}
