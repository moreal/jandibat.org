{
  pkgs,
  buildGoModule,
  nodejs,
  yarnBerry,
  yarnDeps,
}:
let
  mkGoPayload = name: command: buildGoModule {
    pname = "jandibat-${name}-payload";
    version = "1";
    src = ../apps/api;
    subPackages = [ "./cmd/${command}" ];
    vendorHash = "sha256-sjRa+0G+2JM8XVJj4P8khyvUGopIo2xO//tmA+77Z2I=";
    env.CGO_ENABLED = "0";
    ldflags = [ "-buildid=" ];
    doCheck = false;
  };

  webSource = pkgs.lib.fileset.toSource {
    root = ../.;
    fileset = pkgs.lib.fileset.unions [
      ../package.json
      ../yarn.lock
      ../.yarnrc.yml
      ../apps/web
      ../packages/contracts
      ../packages/custom-provider-sdk
      ../packages/solid-relay
    ];
  };
in
{
  api-payload = mkGoPayload "api" "server";
  worker-payload = mkGoPayload "worker" "worker";
  maintenance-payload = mkGoPayload "maintenance" "maintenance";

  web-payload = pkgs.stdenvNoCC.mkDerivation {
    pname = "jandibat-web-payload";
    version = "1";
    src = webSource;
    inherit (yarnDeps) yarnOfflineCache;
    missingHashes = ./yarn-missing-hashes.json;
    YARN_APPROVED_GIT_REPOSITORIES = "**";
    YARN_ENABLE_NETWORK = "false";
    YARN_ENABLE_SCRIPTS = "true";
    YARN_NPM_MINIMAL_AGE_GATE = "0";
    nativeBuildInputs = [
      nodejs
      yarnBerry
      yarnBerry.yarnBerryConfigHook
    ];
    dontYarnBerryPatchShebangs = true;
    buildPhase = ''
      runHook preBuild
      yarn install --immutable --immutable-cache
      yarn workspace @jandibat/web build
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      mkdir -p "$out/dist"
      cp -R apps/web/dist/client "$out/dist/client"
      runHook postInstall
    '';
  };
}
