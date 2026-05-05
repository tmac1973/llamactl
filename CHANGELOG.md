# Changelog

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
