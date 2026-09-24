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
            # The exact upstream tag already includes its release bundle.
            # Rebuilding it here invokes esbuild-wasm and can stall on Darwin;
            # keep the nixpkgs Yarn derivation and its Berry dependency hooks.
            dontBuild = true;
            installPhase = ''
              runHook preInstall
              install -Dm755 packages/yarnpkg-cli/bin/yarn.js "$out/bin/yarn"
              substituteInPlace "$out/bin/yarn" \
                --replace-fail '#!/usr/bin/env node' '#!${nodejs}/bin/node'
              # The upstream source tree redirects Yarn through .yarnrc.yml;
              # validate the installed bundle itself, not that dev launcher.
              test "$(YARN_IGNORE_PATH=1 "$out/bin/yarn" --version)" = 4.18.0
              runHook postInstall
            '';
          });

          buildGoModule = pkgs.buildGoModule.override { inherit go; };

          staticcheck = (pkgs.go-tools.override { inherit buildGoModule; }).overrideAttrs (_: {
            # Upstream go/ir.TestStdlib has a fixed 10-minute deadline and
            # times out while the pinned toolchain builds on Darwin. Keep all
            # other upstream tests, then exercise the installed analyzer on a
            # Go 1.27 fixture and all API packages in our own CI gates.
            checkFlags = [ "-skip=^TestStdlib$" ];
          });

          exhaustive = (pkgs.exhaustive.override { inherit buildGoModule; }).overrideAttrs (old: {
            patches = old.patches ++ [ ./nix/exhaustive-go127.patch ];
            vendorHash = "sha256-rJB0xI6ZUwHd1Tk62+1jI0ymUYnExGPoND7h7cN7Vsg=";
          });

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
            version = "0.17.0";
            src = pkgs.fetchFromGitHub {
              owner = "Goldziher";
              repo = "scythe";
              tag = "v0.17.0";
              hash = "sha256-DnlaEqSQEqa79fLYF9qsV5VX/g0edevbERwdZOkrajI=";
            };
            cargoHash = "sha256-MoHMTVUcEagJHG5TU6ugeQ5Vc+z5eY1YwfKAgvQl6zE=";
            patches = [ ./nix/patches/scythe-cockroach.patch ];
            nativeCheckInputs = [ pkgs.ruby ];
            cargoBuildFlags = [ "--package=scythe-cli" ];
            cargoTestFlags = [
              "--package=scythe-cli"
              "--package=scythe-core"
              "--package=scythe-codegen"
            ];
            # These two upstream Python-output tests require Goldziher/poly,
            # which is not part of the generator runtime. Keep all other CLI
            # tests; this repository checks emitted Go and live SQL separately.
            checkFlags = [
              "--skip=report_generated_code_validation_returns_true_on_a_real_tool_failure"
              "--skip=generate_validate_output_reports_validated_when_the_tool_is_present"
            ];
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
            staticcheck
            exhaustive
            goCheckSumtype
          ];
        in
        {
          inherit
            buildGoModule
            go
            goCheckSumtype
            exhaustive
            nodejs
            pkgs
            scythe
            shellPackages
            staticcheck
            yarnBerry
            yarnDeps
            ;
        };
    in
    {
      packages = forAllSystems (system:
        let
          toolchain = mkToolchain system;
        in
        if toolchain.pkgs.stdenv.hostPlatform.isLinux then
          import ./nix/images.nix {
            inherit (toolchain) buildGoModule nodejs pkgs yarnBerry yarnDeps;
          }
        else
          { });

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
          scythe-compatibility = toolchain.pkgs.runCommand "jandibat-scythe-compatibility" {
            nativeBuildInputs = [ toolchain.scythe ];
          } ''
            cp -R ${./nix/fixtures/scythe-cockroach-repro} repro
            chmod -R u+w repro
            scythe generate --config repro/scythe.toml
            grep -Fq 'UPSERT INTO probe_items' repro/generated/queries.go
            grep -Fq 'type DBTX interface' repro/generated/queries.go
            touch "$out"
          '';
          yarn-dependencies = toolchain.yarnDeps.webDependencies;
        });
    };
}
