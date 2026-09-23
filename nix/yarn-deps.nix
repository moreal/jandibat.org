{
  pkgs,
  nodejs,
  yarn,
}:
let
  # Yarn does not record checksums for optional packages that are unavailable on
  # the installer's platform. The pinned fetcher generates this map from the
  # current lockfile so the Nix cache can still include every supported target.
  missingHashes = ./yarn-missing-hashes.json;

  yarnOfflineCache = yarn.fetchYarnBerryDeps {
    yarnLock = ../yarn.lock;
    hash = "sha256-UmV5dKLWeLMwo6fRGkkyGNR6PTi42W0vD5g9YIfVzF8=";
    inherit missingHashes;
  };
in
{
  inherit yarnOfflineCache;

  webDependencies = pkgs.stdenvNoCC.mkDerivation {
    pname = "jandibat-web-dependencies";
    version = "1";
    src = pkgs.lib.fileset.toSource {
      root = ../.;
      fileset = pkgs.lib.fileset.unions [
        ../package.json
        ../yarn.lock
        ../.yarnrc.yml
        ../apps/web/package.json
        ../apps/web/tools/openapi-typescript-cli
        ../packages/contracts/package.json
        ../packages/custom-provider-sdk/package.json
        ../packages/solid-relay/package.json
      ];
    };
    inherit yarnOfflineCache;
    inherit missingHashes;
    YARN_APPROVED_GIT_REPOSITORIES = "**";
    YARN_ENABLE_SCRIPTS = "true";
    YARN_NPM_MINIMAL_AGE_GATE = "0";
    nativeBuildInputs = [
      nodejs
      yarn
      yarn.yarnBerryConfigHook
    ];
    dontYarnBerryPatchShebangs = true;
    dontBuild = true;
    installPhase = ''
      runHook preInstall
      mkdir -p "$out"
      cp yarn.lock "$out/yarn.lock"
      runHook postInstall
    '';
  };
}
