{
  pkgs,
  buildGoModule,
  nodejs,
  yarnBerry,
  yarnDeps,
}:
let
  mkGoPayload = name: commands: buildGoModule {
    pname = "jandibat-${name}-payload";
    version = "1";
    src = ../apps/api;
    subPackages = map (command: "./cmd/${command}") commands;
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

  runtimeEnvironment = [
    "PATH=/bin"
    "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt"
  ];

  mkGoImage = name: program: port: payload: extraPrograms:
    pkgs.dockerTools.buildLayeredImage {
      name = "jandibat-${name}";
      tag = "nix";
      created = "1970-01-01T00:00:01Z";
      contents = pkgs.runCommand "${name}-image-root" { } ''
        mkdir -p "$out/bin" "$out/etc/ssl/certs" "$out/tmp"
        ln -s ${payload}/bin/${program} "$out/bin/${program}"
        ${pkgs.lib.concatMapStringsSep "\n" (extra: ''ln -s ${payload}/bin/${extra} "$out/bin/${extra}"'') extraPrograms}
        ln -s ${pkgs.busybox}/bin/busybox "$out/busybox"
        ln -s ${pkgs.cacert}/etc/ssl/certs/ca-certificates.crt "$out/etc/ssl/certs/ca-certificates.crt"
      '';
      fakeRootCommands = ''chmod 1777 tmp'';
      config = {
        User = "65532:65532";
        Entrypoint = [ "/bin/${program}" ];
        Env = runtimeEnvironment;
        ExposedPorts = { "${toString port}/tcp" = { }; };
      };
    };
in
rec {
  # Copied into restore-tools without a Nix store closure.
  restore-tools-busybox = pkgs.pkgsStatic.busybox;

  api-payload = mkGoPayload "api" [ "server" "metrics-proxy" ];
  worker-payload = mkGoPayload "worker" [ "worker" ];
  maintenance-payload = mkGoPayload "maintenance" [ "maintenance" ];

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

  api-image = mkGoImage "api" "server" 8080 api-payload [ "metrics-proxy" ];
  worker-image = mkGoImage "worker" "worker" 8081 worker-payload [ ];
  maintenance-image = mkGoImage "maintenance" "maintenance" 8082 maintenance-payload [ ];

  web-image = pkgs.dockerTools.buildLayeredImage {
    name = "jandibat-web";
    tag = "nix";
    created = "1970-01-01T00:00:01Z";
    contents = pkgs.runCommand "web-image-root" { } ''
      mkdir -p "$out/bin" "$out/etc/nginx" "$out/etc/ssl/certs" "$out/usr/share/nginx" "$out/tmp"
      cp ${./web-start.sh} "$out/bin/web-start"
      chmod 755 "$out/bin/web-start"
      cp ${./nginx.conf} "$out/etc/nginx/nginx.conf"
      cp ${../apps/web/nginx.conf} "$out/etc/nginx/site.conf"
      cp ${../apps/web/docker-entrypoint.d/40-runtime-config.sh} "$out/etc/nginx/runtime-config.sh"
      ln -s ${pkgs.nginxMainline}/conf/mime.types "$out/etc/nginx/mime.types"
      ln -s ${pkgs.nginxMainline}/bin/nginx "$out/bin/nginx"
      ln -s ${web-payload}/dist/client "$out/usr/share/nginx/html"
      ln -s ${pkgs.busybox}/bin/busybox "$out/busybox"
      for tool in sh grep awk sed; do
        ln -s ${pkgs.busybox}/bin/$tool "$out/bin/$tool"
      done
      ln -s ${pkgs.cacert}/etc/ssl/certs/ca-certificates.crt "$out/etc/ssl/certs/ca-certificates.crt"
    '';
    fakeRootCommands = ''chmod 1777 tmp'';
    config = {
      User = "101:101";
      Entrypoint = [ "/bin/web-start" ];
      Env = runtimeEnvironment;
      ExposedPorts = { "8080/tcp" = { }; };
    };
  };
}
