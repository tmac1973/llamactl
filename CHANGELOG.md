# Changelog

## [2.2.0](https://github.com/tmac1973/llama-toolchest/compare/v2.1.2...v2.2.0) (2026-05-10)


### Features

* **hf:** mark downloaded models and gate downloads by free disk space ([3f07aa2](https://github.com/tmac1973/llama-toolchest/commit/3f07aa230711f8740d33c35356f448f5dd42f2a4))
* **hf:** mark downloaded models and gate downloads by free disk space ([01b0af1](https://github.com/tmac1973/llama-toolchest/commit/01b0af181b26b01196a793e6c576c6492a5b6a22))

## [2.1.2](https://github.com/tmac1973/llama-toolchest/compare/v2.1.1...v2.1.2) (2026-05-09)


### Bug Fixes

* **api:** make /v1 and /api/models endpoints behave consistently ([b9519e9](https://github.com/tmac1973/llama-toolchest/commit/b9519e992a83d8d71deebc33d74c774854c227cc))
* **api:** make /v1 and /api/models endpoints behave consistently ([d1cc197](https://github.com/tmac1973/llama-toolchest/commit/d1cc197a671f525535ad6227964a6f573a2a8f58))

## [2.1.1](https://github.com/tmac1973/llama-toolchest/compare/v2.1.0...v2.1.1) (2026-05-09)


### Bug Fixes

* **benchmark:** forward HF_TOKEN and HF_HOME to llama-benchy ([376194c](https://github.com/tmac1973/llama-toolchest/commit/376194ce6c749962260f4c859890133a2421bd46))
* **setup:** install uv in container images for llama-benchy ([ea412fe](https://github.com/tmac1973/llama-toolchest/commit/ea412fe4a2a21c19479bb88e3371a7f2dddab6a2))
* **ui:** unique download progress slot IDs per HF model ([8d2073f](https://github.com/tmac1973/llama-toolchest/commit/8d2073f71330ea0872236686f477206bbe5284a4))
* **ui:** unique download progress slot IDs per HF model ([3f2e414](https://github.com/tmac1973/llama-toolchest/commit/3f2e414d4d3a9f453f671041baa41f26ac1b97b7))

## [2.1.0](https://github.com/tmac1973/llama-toolchest/compare/v2.0.0...v2.1.0) (2026-05-07)


### Features

* **setup:** auto-route up/down/logs and harden host cuda install ([bafbb11](https://github.com/tmac1973/llama-toolchest/commit/bafbb116bce7855f21303e1d3847965cc1199eed))
* **setup:** auto-route up/down/logs and harden host cuda install ([3585dcc](https://github.com/tmac1973/llama-toolchest/commit/3585dcc814dbd2ac03582c087a2a4785da445127))

## [2.0.0](https://github.com/tmac1973/llama-toolchest/compare/v1.9.3...v2.0.0) (2026-05-07)


### ⚠ BREAKING CHANGES

* **ui:** fold the single-run path into a 1-cell job builder (pass 7b)
* **benchmark:** drop llama-bench, rename presets to internal-*

### Features

* **api:** /api/benchmark-jobs endpoints + SSE progress + run filters ([8399d88](https://github.com/tmac1973/llama-toolchest/commit/8399d88c5b19979ff72a5da28e1814b77ded5022))
* **api+ui:** About Benchmarks modal renders live data from server ([952c5e4](https://github.com/tmac1973/llama-toolchest/commit/952c5e4750a9dcafe87b3a2be56e39c0f7ddeb8e))
* **api+ui:** full CSV (cells/summary) + JSON exports for runs and jobs ([874b449](https://github.com/tmac1973/llama-toolchest/commit/874b4492fc0050f0be42ee1f7df3fa6d1d5b69c7))
* **benchmark+ui:** build pre-flight guard, live job updates ([296a35f](https://github.com/tmac1973/llama-toolchest/commit/296a35f44e6ea6812ab3c7fbf060dc85230bb672))
* **benchmark:** drop llama-bench, rename presets to internal-* ([149ad5e](https://github.com/tmac1973/llama-toolchest/commit/149ad5e6ee87c0cbef77392a8b4fb7c934029e76))
* **benchmark:** integrate llama-benchy alongside existing presets ([3652c78](https://github.com/tmac1973/llama-toolchest/commit/3652c788d9121b82745f880876a25d44005a72a5))
* **benchmark:** job model + v2 storage envelope with v1→v2 migration ([4286591](https://github.com/tmac1973/llama-toolchest/commit/4286591235a32e626449b1a6ad8f5f15f262b0a9))
* **benchmark:** JobQueue with sequential per-cell orchestration ([9182c72](https://github.com/tmac1973/llama-toolchest/commit/9182c72e76582c0ec8cae30f9a52164899491095))
* **benchmark:** snapshot the active build on every run ([827570c](https://github.com/tmac1973/llama-toolchest/commit/827570c14addb60cdc70ee3f512990906d6c2504))
* **ui:** fold the single-run path into a 1-cell job builder (pass 7b) ([8e32975](https://github.com/tmac1973/llama-toolchest/commit/8e32975f684d54920cabbbff8db79489f65b0f8e))
* **ui:** jobs list, detail matrix, and new-job form (pass 7a) ([d4b1c15](https://github.com/tmac1973/llama-toolchest/commit/d4b1c15ddacba414156fa4c2b2ad01ad11d6396c))
* **ui:** override fields use dropdowns to match the model config form ([4d4c3e7](https://github.com/tmac1973/llama-toolchest/commit/4d4c3e751d0ee86a642a1926376f788fb562e5d6))
* **ui:** show model quant + preserve open cell-detail across job poll ([75405dc](https://github.com/tmac1973/llama-toolchest/commit/75405dc078e70f10a75c60a2ad6aad9936ea55a1))
* **ui:** wider model column, tooltips, build column, client-side sort ([70dbcbc](https://github.com/tmac1973/llama-toolchest/commit/70dbcbc094283b93d17ea08bea3180efe369cb8a))


### Bug Fixes

* **benchmark:** include sentencepiece + tiktoken in uvx environment ([113b081](https://github.com/tmac1973/llama-toolchest/commit/113b081f7d587cd3f96052d9fdfcaac3fe884a82))
* **setup:** host install summary reports the configured port ([43ef7e2](https://github.com/tmac1973/llama-toolchest/commit/43ef7e21e19b98555532efcf29928e91b9fc83b1))
* **ui:** drop list polling, push live updates via OOB swaps from detail ([319d2b6](https://github.com/tmac1973/llama-toolchest/commit/319d2b6c9926e2495676f00aee1812305573b4c2))
* **ui:** pivot compare details table — one row per run ([a2d7d94](https://github.com/tmac1973/llama-toolchest/commit/a2d7d947ecb00c24021af84067b8364b91ca3179))
* **ui:** re-fetch detail directly after list refresh, not via event trigger ([b9897c6](https://github.com/tmac1973/llama-toolchest/commit/b9897c6380e13d43019161f27ee6bfe37c84aa66))
* **ui:** remove redundant Benchmark Results section, scope bulk actions ([356a9d4](https://github.com/tmac1973/llama-toolchest/commit/356a9d4f501037a2c5d37ed50b18feffc24533b5))
* **ui:** show "f16" for default KV cache quant instead of dash/hidden ([4f8dc63](https://github.com/tmac1973/llama-toolchest/commit/4f8dc63ba2b4092293f0d527335bd2c9b1be38e3))
* **ui:** use htmx.ajax for compare swap so inline &lt;script&gt; runs ([45756cb](https://github.com/tmac1973/llama-toolchest/commit/45756cb77c5716e837aa48a1986897b219eef4f4))

## [1.9.3](https://github.com/tmac1973/llama-toolchest/compare/v1.9.2...v1.9.3) (2026-05-06)


### Bug Fixes

* **server:** restart picks up the latest active build, settings persist ([27aaa87](https://github.com/tmac1973/llama-toolchest/commit/27aaa870aefbee45054e186d192591bd1017c2ce))

## [1.9.2](https://github.com/tmac1973/llama-toolchest/compare/v1.9.1...v1.9.2) (2026-05-06)


### Bug Fixes

* **vram:** treat ctx_size=0 as the model's trained context, not 2048 ([d079a5e](https://github.com/tmac1973/llama-toolchest/commit/d079a5ed743d9194867bba6bdbad41c2e994c23c))

## [1.9.1](https://github.com/tmac1973/llama-toolchest/compare/v1.9.0...v1.9.1) (2026-05-06)


### Bug Fixes

* **config:** stop env-var leak that lost downloaded models ([ffbe58e](https://github.com/tmac1973/llama-toolchest/commit/ffbe58e8fd3c9aaf89bab7dc8d9be8c19174eff8))

## [1.9.0](https://github.com/tmac1973/llama-toolchest/compare/v1.8.0...v1.9.0) (2026-05-06)


### Features

* **models:** per-model parallel request slots ([6c25ebd](https://github.com/tmac1973/llama-toolchest/commit/6c25ebd5c4181cb7b6cf5006d53951384f52acb9))

## [1.8.0](https://github.com/tmac1973/llama-toolchest/compare/v1.7.1...v1.8.0) (2026-05-05)


### Features

* **browse:** add HuggingFace link icon to search results ([f686085](https://github.com/tmac1973/llama-toolchest/commit/f686085581311aef3b14f9f4fa970abf5373faf5))
* **models:** add HuggingFace link icon next to model name ([9d462fc](https://github.com/tmac1973/llama-toolchest/commit/9d462fc2d877dc143914981c74e89f98494b2956))

## [1.7.1](https://github.com/tmac1973/llama-toolchest/compare/v1.7.0...v1.7.1) (2026-05-05)


### Bug Fixes

* **benchmarks:** reattach progress UI when returning to the tab ([3df3f4f](https://github.com/tmac1973/llama-toolchest/commit/3df3f4fec28cc88143b8a7542c219c193f6840de))
* **monitor:** strip card/GPU prefix from rocm-smi device field ([0e701e7](https://github.com/tmac1973/llama-toolchest/commit/0e701e796f41f068a8cf72b442ea1729589ce77b))

## [1.7.0](https://github.com/tmac1973/llama-toolchest/compare/v1.6.0...v1.7.0) (2026-05-05)


### Features

* add deps command and SDK section in status --host ([f7ec150](https://github.com/tmac1973/llama-toolchest/commit/f7ec1505e3baf63d02099d4bada89a6dfda8cf1d))
* add migrate command for switching between container and host installs ([c090de2](https://github.com/tmac1973/llama-toolchest/commit/c090de2771c4a33c5df3a049219e839dbbcf5c87))
* **host:** support multi-backend SDK installs (--cuda/--rocm/--vulkan) ([c8d2a76](https://github.com/tmac1973/llama-toolchest/commit/c8d2a766697a762aff508064ef89bee12a6f7f6a))


### Bug Fixes

* emit mode-specific speculative decoding flags ([b769934](https://github.com/tmac1973/llama-toolchest/commit/b769934b7a453715494faf403bc971abf72bccb1))
* **host:** use glslc package on Debian instead of glslang-tools ([ab2d148](https://github.com/tmac1973/llama-toolchest/commit/ab2d148ace6b1cee4058c4f3ab6107080b598c99))
* **migrate:** translate mmproj_path/draft_model_path across the boundary ([a19eade](https://github.com/tmac1973/llama-toolchest/commit/a19eade8c11f3a9298234d07a3308309a7174033))

## [1.6.0](https://github.com/tmac1973/llama-toolchest/compare/v1.5.1...v1.6.0) (2026-05-05)


### Features

* redesign models tab as flat list of cards ([d82921b](https://github.com/tmac1973/llama-toolchest/commit/d82921ba0ebab7d24460da557872e888612eb8cb))
* restrict vulkan builds to host mode and expand vulkan deps ([b821347](https://github.com/tmac1973/llama-toolchest/commit/b82134715264e7f45488c767f62a6b1b76dfb83f))

## [1.5.1](https://github.com/tmac1973/llama-toolchest/compare/v1.5.0...v1.5.1) (2026-05-04)


### Bug Fixes

* auto-refresh model and build lists after state changes ([e7e9859](https://github.com/tmac1973/llama-toolchest/commit/e7e9859daa61d3f0d00980041c3b8d361e30f2b5))

## [1.5.0](https://github.com/tmac1973/llama-toolchest/compare/v1.4.2...v1.5.0) (2026-05-01)


### Features

* GPU allocation map uses peak VRAM (weights + KV cache) ([728c8e5](https://github.com/tmac1973/llama-toolchest/commit/728c8e5131bdf3caaea964a7d64c0b019ce56464))

## [1.4.2](https://github.com/tmac1973/llama-toolchest/compare/v1.4.1...v1.4.2) (2026-05-01)


### Bug Fixes

* server log Clear button now drops the buffered history ([280d3b4](https://github.com/tmac1973/llama-toolchest/commit/280d3b49ff57efedd32c001a509d398be13427b5))

## [1.4.1](https://github.com/tmac1973/llama-toolchest/compare/v1.4.0...v1.4.1) (2026-05-01)


### Bug Fixes

* dashboard "restart needed" false-positive and Available Models layout ([47bccff](https://github.com/tmac1973/llama-toolchest/commit/47bccffa78c4b76e33534580308cc00b163967b4))

## [1.4.0](https://github.com/tmac1973/llama-toolchest/compare/v1.3.0...v1.4.0) (2026-04-30)


### Features

* setup.sh quick now upgrades the package without rebuilding the image ([b83e6f2](https://github.com/tmac1973/llama-toolchest/commit/b83e6f2cf6a9debeb8c99f288bc965b509a65c71))

## [1.3.0](https://github.com/tmac1973/llama-toolchest/compare/v1.2.1...v1.3.0) (2026-04-30)


### Features

* show version in sidebar under "Inference Manager" ([e3b1235](https://github.com/tmac1973/llama-toolchest/commit/e3b1235739661d76a70c0ed95929c5a42ce223e7))


### Bug Fixes

* model config: don't reset speculative decoding fields on every save ([795e2b2](https://github.com/tmac1973/llama-toolchest/commit/795e2b2acf4b8b46cd0570a68fc40363f4b1a824))

## [1.2.1](https://github.com/tmac1973/llama-toolchest/compare/v1.2.0...v1.2.1) (2026-04-30)


### Bug Fixes

* don't warn about port conflicts caused by our own container ([2f71f95](https://github.com/tmac1973/llama-toolchest/commit/2f71f958e719d8e0f239b5ae66b1f3261abaeae1))
* equalize Info/Delete button heights on Builds page ([9f0ab74](https://github.com/tmac1973/llama-toolchest/commit/9f0ab74d8c858bc6d9faa7b8f86440ffbffb779b))
* migrate_legacy_volume removes the pre-rename container too ([cbea639](https://github.com/tmac1973/llama-toolchest/commit/cbea639349800a699fd2cbcc597ba7aaab4d41cc))
* portable container-existence check (Docker compatibility) ([b3327d0](https://github.com/tmac1973/llama-toolchest/commit/b3327d005c0f85da562a331239d10ca6b68591dc))
* silence Compose warnings on migrated install ([5bf3fe6](https://github.com/tmac1973/llama-toolchest/commit/5bf3fe652ec09f0a0d6cb6dfcf0d66c25c03156a))

## [1.2.0](https://github.com/tmac1973/llama-toolchest/compare/v1.1.0...v1.2.0) (2026-04-30)


### Features

* editable models directory in Settings ([05a2637](https://github.com/tmac1973/llama-toolchest/commit/05a2637f3a3127624532fc244e020065e5416363))


### Bug Fixes

* detect distro family before host install dispatch ([ded3367](https://github.com/tmac1973/llama-toolchest/commit/ded3367550051bf5047a0f27dda9994dbafdcd5f))
* drop unused openblas Recommends from package ([c7e8675](https://github.com/tmac1973/llama-toolchest/commit/c7e8675244c1057bb327fcbdcca7ba692acbf61a))

## [1.1.0](https://github.com/tmac1973/llama-toolchest/compare/v1.0.0...v1.1.0) (2026-04-29)


### Features

* --host install now defaults to fetching released .deb/.rpm ([49c40f6](https://github.com/tmac1973/llama-toolchest/commit/49c40f658a33caa09361f9eacc2fd4633b8d72e5))
* Dockerfiles default to installing released package ([21572e2](https://github.com/tmac1973/llama-toolchest/commit/21572e2765ae9cf6247dfcbea00ad1c07b8558c0))

## 1.0.0 (2026-04-29)


### ⚠ BREAKING CHANGES

* Existing container deployments must run ./setup.sh rebuild to migrate the llamactl-data volume to llama-toolchest-data; .env files using LLAMACTL_* are auto-rewritten to LLAMA_TOOLCHEST_*.

### Features

* containerless host install and rename to llama-toolchest ([52e5c46](https://github.com/tmac1973/llama-toolchest/commit/52e5c46f238d89ab8019ba209845ea9474daa7f2))
