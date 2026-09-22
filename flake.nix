{
  description = "jandibat.org reproducible development toolchain";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/acdf337c2bef0ee8bda5fea34ae879095ffba475";

  outputs =
    { nixpkgs, ... }:
    let
      systems = [
        "aarch64-darwin"
        "x86_64-linux"
        "aarch64-linux"
      ];

      forAllSystems = nixpkgs.lib.genAttrs systems;

      releaseSources = {
        aarch64-darwin = {
          go = {
            url = "https://go.dev/dl/go1.27.1.darwin-arm64.tar.gz";
            hash = "sha256-7iFdV+DsJpxgzJzspo5r2jIbqe5a/iT0sJiHA8LYfRI=";
          };
          node = {
            url = "https://nodejs.org/dist/v24.21.0/node-v24.21.0-darwin-arm64.tar.xz";
            hash = "sha256-YjnUz5LYZEh+yM02FQOPe2fn9Yt3shzS8J6p+9aAZf4=";
          };
        };
        x86_64-linux = {
          go = {
            url = "https://go.dev/dl/go1.27.1.linux-amd64.tar.gz";
            hash = "sha256-Y9M58NpatTY1pW8kkKeYTf4S38/yKtdJ9j7a9ZAWhEU=";
          };
          node = {
            url = "https://nodejs.org/dist/v24.21.0/node-v24.21.0-linux-x64.tar.xz";
            hash = "sha256-/Y5Z1aURUQ9qKYr7VI8Yx9KxvkBNi0on2U++SfVsstY=";
          };
        };
        aarch64-linux = {
          go = {
            url = "https://go.dev/dl/go1.27.1.linux-arm64.tar.gz";
            hash = "sha256-NFC0Wj+e6FaHknNqXF5wofLps2w1qPdJWMA+UdfZK+w=";
          };
          node = {
            url = "https://nodejs.org/dist/v24.21.0/node-v24.21.0-linux-arm64.tar.xz";
            hash = "sha256-atEyXtvbVknDebdaI3FHpmbJXU+a6NNA/vLRV10omtI=";
          };
        };
      };

      mkPkgs = system: import nixpkgs { inherit system; };

      mkToolchain = system:
        let
          pkgs = mkPkgs system;
          sources = releaseSources.${system};

          go = pkgs.stdenvNoCC.mkDerivation {
            pname = "go";
            version = "1.27.1";
            inherit (pkgs.stdenv.hostPlatform.go) GOARCH GOOS;
            CGO_ENABLED = 1;
            src = pkgs.fetchurl sources.go;
            dontUnpack = true;
            nativeBuildInputs = [ pkgs.gnutar ];
            installPhase = ''
              runHook preInstall
              mkdir -p "$out/share"
              tar -xzf "$src" -C "$out/share"
              mkdir -p "$out/bin"
              for executable in "$out/share/go/bin/"*; do
                ln -s "$executable" "$out/bin/$(basename "$executable")"
              done
              runHook postInstall
            '';
          };

          nodejs = pkgs.stdenvNoCC.mkDerivation {
            pname = "nodejs";
            version = "24.21.0";
            src = pkgs.fetchurl sources.node;
            dontUnpack = true;
            nativeBuildInputs =
              [ pkgs.gnutar pkgs.xz ]
              ++ pkgs.lib.optionals pkgs.stdenv.hostPlatform.isLinux [ pkgs.autoPatchelfHook ];
            buildInputs = pkgs.lib.optionals pkgs.stdenv.hostPlatform.isLinux [ pkgs.stdenv.cc.cc.lib ];
            installPhase = ''
              runHook preInstall
              mkdir -p "$out"
              tar -xJf "$src" --strip-components=1 -C "$out"
              runHook postInstall
            '';
          };

          yarnBerry = (pkgs.yarn-berry_4.override { inherit nodejs; }).overrideAttrs (_old: {
            version = "4.18.0";
            src = pkgs.fetchFromGitHub {
              owner = "yarnpkg";
              repo = "berry";
              tag = "@yarnpkg/cli/4.18.0";
              hash = "sha256-pO89wh17cW9/RGKjo70yiefr+9nlJAQs4ZEdUnzdgQM=";
            };
          });

          buildGoModule = pkgs.buildGoModule.override { inherit go; };

          goCheckSumtype = buildGoModule {
            pname = "go-check-sumtype";
            version = "0.5.0";
            src = pkgs.fetchFromGitHub {
              owner = "alecthomas";
              repo = "go-check-sumtype";
              tag = "v0.5.0";
              hash = "sha256-1DcZWUZ/tNCF6iu997jDkO7KeyvNoVonWSFGUCuoPt0=";
            };
            vendorHash = "sha256-2+t/+rvVxqMKz1wlBFkFGfGfx86Q3dMk8rjVqv+/m4U=";
          };

          scythe = pkgs.rustPlatform.buildRustPackage {
            pname = "scythe";
            version = "0.9.0";
            src = pkgs.fetchFromGitHub {
              owner = "Goldziher";
              repo = "scythe";
              tag = "v0.9.0";
              hash = "sha256-EfhJ9uRseSc8PDH/svhnFJNGHAPTe4uS1XgiL/SG+Fc=";
            };
            cargoHash = "sha256-Ry5mqeH9pbCPcsUL0GkIo2ulNSo+YaxrV6P8D+T2KlE=";
            cargoBuildFlags = [ "--package=scythe-cli" ];
            cargoTestFlags = [ "--package=scythe-cli" ];
            preCheck = ''
              cargo run --offline --package test-generator -- \
                --fixtures testing_data \
                --output crates/scythe-cli/tests/generated
            '';
          };

          yarnDeps = import ./nix/yarn-deps.nix {
            inherit nodejs pkgs;
            yarn = yarnBerry;
          };

          shellPackages = [
            go
            nodejs
            yarnBerry
            scythe
            pkgs.go-tools
            pkgs.exhaustive
            goCheckSumtype
          ];
        in
        {
          inherit
            go
            goCheckSumtype
            nodejs
            pkgs
            scythe
            shellPackages
            yarnBerry
            yarnDeps
            ;
        };
    in
    {
      devShells = forAllSystems (system:
        let
          toolchain = mkToolchain system;
        in
        {
          default = toolchain.pkgs.mkShell {
            packages = toolchain.shellPackages;
            shellHook = ''
              export GOROOT=${toolchain.go}/share/go
              export GOTOOLCHAIN=local
              unset GOBIN

              export YARN_APPROVED_GIT_REPOSITORIES='**'
              export YARN_ENABLE_SCRIPTS=true
              export YARN_NPM_MINIMAL_AGE_GATE=0
              export YARN=yarn
            '';
          };
        });

      checks = forAllSystems (system:
        let
          toolchain = mkToolchain system;
        in
        {
          toolchain-interface = toolchain.pkgs.runCommand "jandibat-toolchain-interface" {
            nativeBuildInputs = toolchain.shellPackages;
          } ''
            sh ${./nix/flake-interface-test.sh}
            touch "$out"
          '';
          yarn-dependencies = toolchain.yarnDeps.webDependencies;
        });
    };
}
