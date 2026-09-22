{
  pkgs,
  nodejs,
  yarn,
}:
let
  # Yarn does not record checksums for optional packages that are unavailable on
  # the installer's platform. The pinned fetcher generates this map from the
  # current lockfile so the Nix cache can still include every supported target.
  missingHashes = pkgs.writeText "jandibat-yarn-missing-hashes.json" ''
    {
      "@dom-expressions/compiler-darwin-arm64@npm:0.50.0-next.40": "fecf7a2b3e00dab35857fd03f3ff3770a52992a62272e0eb647ffc338a2105003ae142b91d8940f00a10ed0b32356b272dbdd80b21832879427c1fb83fed8983",
      "@dom-expressions/compiler-darwin-x64@npm:0.50.0-next.40": "d03f88893b0f3628f16af09d913ad49f337ce4f7e8985abd1aff0daea5fff0992a638f1330895d81f50b73e133c552eeaac8cf58707b4a2e764ff586673e464b",
      "@dom-expressions/compiler-linux-arm64-gnu@npm:0.50.0-next.40": "a0be89592eec41b1e9b5dc4172ed26ae242b13a373176494f27e88aa355017f02625703ffa7d723002a4c0f9375ab0be8ca04e56c3368004d3317eef9ed5d92a",
      "@dom-expressions/compiler-linux-x64-gnu@npm:0.50.0-next.40": "d800edfe44064c5227b7845130d63f9b1f4916b9e6604673fefffc6c2f552f659d742f562c974c171c53ff0feaf26177526b9189ff15e8c8da244e0f08779885",
      "@dom-expressions/compiler-win32-x64-msvc@npm:0.50.0-next.40": "257bfbe1c8a281701dca515a8d5f6906e85cfeb6c5476441c5d7890be83a019ab1dc36346e8a095c3f3a5095c80dbe673915bd6045f93a2b2d8f4736a4cf5914",
      "@oxc-parser/binding-android-arm-eabi@npm:0.139.0": "e7c7dfe147d5f30751bd06c71d0baceba87c10a5896ab88ba9643d290effb3581364f9edeca7dba42d8a4fa48af81eec16ee5713d5623b25408d0d73e71c65d7",
      "@oxc-parser/binding-android-arm64@npm:0.139.0": "2bd493d7e6b39e3192a70aa497cb3a84077a2755d2317ed07d7c31e75770c4924492b705fb4373a71317425717cdd20f8ea128afb42343db80fc655e30701ef5",
      "@oxc-parser/binding-darwin-arm64@npm:0.139.0": "42768b4e25229e17b4b5eacd579aefed0efcf3128bd4414c2bf6b1d4d731c3ddb5845d251d778d2dcdfbf40634a6c3d68524a0f3992b3d81fc3063b1e54c8267",
      "@oxc-parser/binding-darwin-x64@npm:0.139.0": "5c969475d1a07be328202df8e5bee8755c2d76f3df2e825c229219169949f5142f2ffcdb128b37aeeeac34e45eb82525bb03adb93f097db73e3679b8b56c4b24",
      "@oxc-parser/binding-freebsd-x64@npm:0.139.0": "54a9980bc3af2bb86dcbda188e625640b2669998956d23c5d9429db60abd312fefbe6ed0627525e820915257185c1c18d02fb2838a21a66acc01d596ea65b540",
      "@oxc-parser/binding-linux-arm-gnueabihf@npm:0.139.0": "93f1ea5ebdd77a47852462cd107d06a139af9b8375389a56038a708da4716c080cd5a5be3432cd18727341a38ff460bf69edeef315200c3a97277c244fa6c1fd",
      "@oxc-parser/binding-linux-arm-musleabihf@npm:0.139.0": "78825a5d6bb1895676d551e930c5961416dc3478ec70e906f6ad213ff8c4372615d488ee182ccfa54a221b89dee1aad7b0206241cae3fa693d19e7ea0716b384",
      "@oxc-parser/binding-linux-arm64-gnu@npm:0.139.0": "1c634faddeeaffd01b57f69438031cf66120755325c384234bd53436a38bbe51da88d9f351973c3e74d23b748c6352316dd06f7e174663b8412868527fced0eb",
      "@oxc-parser/binding-linux-arm64-musl@npm:0.139.0": "c691eb8b767efc2e26d85ebfa50d75ebcc4a05fe86c24337c09c15602f9f9d53991f8e6bb9b4eafc9b9a4f3c4750c3a23674462f13378a327f699c8f0c258730",
      "@oxc-parser/binding-linux-ppc64-gnu@npm:0.139.0": "c3eaad63b7759b862c27c848abd01b8ff99420cb192fba2b8696efa697b4ab9bf789f7bd746e3a049d0b125e82e25eaa99bc7def49d6a491113a1efbaf80ed00",
      "@oxc-parser/binding-linux-riscv64-gnu@npm:0.139.0": "423d9997ad4a26fc7b71e7514f922f1f965a03d5aa4832a5cb9caf8ff261be778d84f9d95531a8349d13b0bf5af805f69d0686a92160cf6289a50e346aa718e6",
      "@oxc-parser/binding-linux-riscv64-musl@npm:0.139.0": "d9de965e322d3e6b3754ba00dcc398a9bd59039023883c4966ae6a769c53d763e9fb893bed807da3d4315aa7f8212f6688aab63f1c6165bf235ce2a497285f64",
      "@oxc-parser/binding-linux-s390x-gnu@npm:0.139.0": "050e23cce0a2643aa9230ec7762a0409a7452a9167245d86ad2b17e2fb795d55b722125c311854e2e61b5b165f8e2a386065e63c8095286d105ff9419f589ed4",
      "@oxc-parser/binding-linux-x64-gnu@npm:0.139.0": "d441d43641461c29d6c06e2392cfad104f4afec042286ab392200ac70f72d68db4f33b021095da94a24129ba4b8d9287bf6443e7d1bc1c9bf7936ec77feeed48",
      "@oxc-parser/binding-linux-x64-musl@npm:0.139.0": "40d388832dc03fc5756a590157c3db1bd2a12bbd12823f330120ccb6b6f4290d84c2db39187762fba1901687c22ec4fccc82c583339a8b0647c8e078e69f7e79",
      "@oxc-parser/binding-openharmony-arm64@npm:0.139.0": "170dcf3e15230f78eb84b9c1d435156e0b832b17f0fbef5dad6b33998e5dc828f988e8ab113db8b63e91fc8dfc302719a08f4706e08e95d30197513e542e597f",
      "@oxc-parser/binding-wasm32-wasi@npm:0.139.0": "b642148ca7cc64e830e2b94a31908366209db48b0cbb8d9dcda41e28d98e15c14dfb28c797d9c119403dfa9fe643fd03cfed9b957fe8edaafb498ee93354deaf",
      "@oxc-parser/binding-win32-arm64-msvc@npm:0.139.0": "5afe1050022a1b7b6c618ecaec6e0bbed8acfb6287c2aab7e084d0437498e1012732c65242fc37519f0b7813ee273ec92b47a583a32453563dc59bd9b8b88d6e",
      "@oxc-parser/binding-win32-ia32-msvc@npm:0.139.0": "f4f879d1f6fc7fc9b7d863059c39ef7913467b409c9601dd9da9ac5364c8f4d4ebb1cedc3640401c9ef54ec3db0f8bca0146684ab3c5d80f2e9653b2cfe248a3",
      "@oxc-parser/binding-win32-x64-msvc@npm:0.139.0": "3656f7c79e951bdf01e1df5ac869bb8d8b1a148f57599742a9dbadd053f0505c08c936802d9127836ceaea208e45e0e9d52e4419a509e15606fbd98dce27862d",
      "@rolldown/binding-android-arm64@npm:1.2.4": "4a17de77efc2a9ff0e7d7323228ec431202b71a71a891f72225716be21f2c7d64a64bf146c8a0c9490dd6a615c13ff5e851487e1a5e331c437c8ff262bf8123e",
      "@rolldown/binding-darwin-arm64@npm:1.2.4": "4bfd5d37947bb0178eee65d48a60419c4c612a08965f1683e3670bb4c051603c01ff6d9d3a0b6e596c1a458b05e0f84ad2d12437aae8eb981743e8595814616f",
      "@rolldown/binding-darwin-x64@npm:1.2.4": "11050e2eeeefebcbf67708c7c732429bd6782edb262a98ac0014dbc840763100fa9efacb002921927052db654130b9657bc7c40edc8a36d206d0fbdcbab77d69",
      "@rolldown/binding-freebsd-x64@npm:1.2.4": "6f75021f01caccc2b7ca8dcd8d6ae3710e5d21a1d14f970b29a9b4f3f09305fc1aac9249c8b5155a93c1127da0d31f52bbf64ff8bab8bfb5d5b54b386fb5a18e",
      "@rolldown/binding-linux-arm-gnueabihf@npm:1.2.4": "427c391b68d4612872d554ceb77396347a61c1489564962b6f67563beaea18106be77b615a91ff850bbee723b45aef75457bc98188d30ae20e55eb891a3f39d9",
      "@rolldown/binding-linux-arm64-gnu@npm:1.2.4": "4f1b94ad7fb060cfd029f85763c6f729597b1cc8d05de8712815f96bb239f2016e02382a16f893b76c96567c0b8a07e6e2443f59fcbc3e9b6483fb80a3e0bc5a",
      "@rolldown/binding-linux-arm64-musl@npm:1.2.4": "42cf55f16918f31ae99ea0a4bda883d3d779b0f62eb6e5ff3e58e977affdbeac89a9b09180614e59788f7ca0dd003a93ad7996682d712425e09999cd59fade99",
      "@rolldown/binding-linux-ppc64-gnu@npm:1.2.4": "7e99c3ee53242552e6b1199fdea7389d9db01107400e0247a9d55e21d00bee7982826cf43d372d364c6c6a2711c2e79e75aa7018fd21ef0d747e92685696a95b",
      "@rolldown/binding-linux-s390x-gnu@npm:1.2.4": "cc01213da8351557e8def0f5e16a8aa4fcb357ae0a029a3ab17352e9a33c6a3a1664faa5505d657b0ef24e3307e10b4ba8bdb53c202e9653c6f7535a56aa0c6b",
      "@rolldown/binding-linux-x64-gnu@npm:1.2.4": "f084a230c58af38d52db6f4394b311e228a80cc1720ead02b7acef7bfba6f2642384557d879fdf9f638d98bf1712d7590c9a4279d6915391d250fc415cca9c3c",
      "@rolldown/binding-linux-x64-musl@npm:1.2.4": "7edad6dd678275504191da68bc911f141778eaf3a432f8e056b41bf41728bb3459844a061213ffbc5d946071c5c3e5e6f1d296b62d26c67bf83fd04ed6f2c6d9",
      "@rolldown/binding-openharmony-arm64@npm:1.2.4": "8d1116a8014e8dc3533f535ef7fa7f643c6bd8e8b21aa809fb4bca0cb91c7e2a797ba5fb73a48c89ee345dd43d224e0f8e547a35daea2e80bc3c1a8cc628ffd9",
      "@rolldown/binding-win32-arm64-msvc@npm:1.2.4": "2a4f0a6cb9279b47d99c99aa28a0116e3f5d4646be7a57f9a970a872e156394e632e3f33476e9fcf200bd5b1526a89df3d4e5480b2c47c814e1c23dbb792615a",
      "@rolldown/binding-win32-x64-msvc@npm:1.2.4": "521ba42f438e7484c87d9bb59ebbbda9f0efafcf41d2310b550a450d6d58d16af58494b3328a281206ec8456e37015cf7664d4d8b6eb87cdfbac05cf24a63f7a",
      "lightningcss-android-arm64@npm:1.33.0": "46c40a0474a7a9ddcdc33e0ea634912894a9360b6ba6f2bc42f1a817d16992784d2fe6f421d293a90ea94fb481503e27fdc54f0540de0b41d7028fe9e9c9e56a",
      "lightningcss-darwin-arm64@npm:1.33.0": "398b770e33f66db9e4c14e4a45d0e93fa59644bd29ce5082de79ee9560a5946321ac55e896aa7fb3f177d218bef3f2dca530e209a857f0f066bf3296f5dc0742",
      "lightningcss-darwin-x64@npm:1.33.0": "132bdab8f7b66dc99730f1ccfcb882880e411420e3f9ca764c348c686112d75962d5d7e928f3bbce7088c1be168d00ea8d722fd909033b807eef191b5a2832c0",
      "lightningcss-freebsd-x64@npm:1.33.0": "05001395776e994ea1254dcc2613f06b44458ba33769e3a1679a0f577e43632af378cc1582f3bd1cb36e480d237066a371bc04233d6c84f8e2fe9979ab4a4032",
      "lightningcss-linux-arm-gnueabihf@npm:1.33.0": "d0f6d993846992cf6db80f2992d0b89ffddd65a8260bbe6a1534bde28510bcfa214e8831df9ccfef5ede006a6368de1649e1d96a3e122169286647b704e08f5f",
      "lightningcss-linux-arm64-gnu@npm:1.33.0": "278a5c553e15ed7645d3e83a3b1515060e98a8742b7008af28d610d6495a09368e73ed8b90bcb537b37249bfc2446ef5b9614b3626222f25153c538909488548",
      "lightningcss-linux-arm64-musl@npm:1.33.0": "7aa2a2ef42e8c713e6e8eb947bf64d5272c6fdbfca7b4eb3adde555357652be27cc0d84d6fef6266543c2d303943a4c4472679d1e258458119b159f0ca868ffe",
      "lightningcss-linux-x64-gnu@npm:1.33.0": "fa57a09af3340dc5f11fdbff820fbb7b08e306e69350a500b523c88f54c1e3a40b3785fed6043d69e2e18bacd4b72410518c9b817ee987c704a80851d9201e45",
      "lightningcss-linux-x64-musl@npm:1.33.0": "4b0cad846f8fbe4ec321a45ef9c0d02cf4277980ac34dcfc7b422b34381e7cb11512819401b01bf029d10aa0fb8a35906ce850ba38d9f43605917ccc9b0b5d4a",
      "lightningcss-win32-arm64-msvc@npm:1.33.0": "5e4b58d7a41fe3c37ce2af7b1a95c3722a6064a84fb6286ba2cf1106651223403b5a5863ecd3eb8fb9732480e612e30d786b859329391b0197dffc88ca18c953",
      "lightningcss-win32-x64-msvc@npm:1.33.0": "7beb4c8580f9ea45fea6d92a8de42f706c4935b2ca68f4f5163a2bd9f6ed1574f7e53686e4927883078b07a4063a9fffdc4b41968568354259066c7a70c156a4"
    }
  '';

  yarnOfflineCache = pkgs.fetchYarnBerryDeps {
    yarnLock = ../yarn.lock;
    hash = "sha256-ydGHKBwD2LpwOtntWJayqkI2NNp8IzNQ1/q6AjCqNg0=";
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
