# Changelog

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
