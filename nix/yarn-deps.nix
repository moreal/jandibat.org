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

  yarnOfflineCache = pkgs.fetchYarnBerryDeps {
    yarnLock = ../yarn.lock;
    hash = "sha256-TK/WQeiWx/mO71skdRtmz8QZm9hMaKKfq+r/5bdzQZE=";
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
        ../packages/contracts/package.json
        ../packages/custom-provider-sdk/package.json
      ];
    };
    inherit yarnOfflineCache;
    inherit missingHashes;
    YARN_APPROVED_GIT_REPOSITORIES = "**";
    YARN_ENABLE_SCRIPTS = "true";
    YARN_LOCKFILE_VERSION_OVERRIDE = "8";
    YARN_NPM_MINIMAL_AGE_GATE = "0";
    nativeBuildInputs = [
      nodejs
      yarn
      pkgs.yarnBerryConfigHook
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
