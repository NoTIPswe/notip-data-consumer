## [2.0.1](https://github.com/NoTIPswe/notip-data-consumer/compare/v2.0.0...v2.0.1) (2026-04-12)


### Bug Fixes

* changed sonarqube properties ([987759d](https://github.com/NoTIPswe/notip-data-consumer/commit/987759db6b1bef098f452eddbd7ecf8873e4ec97))

# [2.0.0](https://github.com/NoTIPswe/notip-data-consumer/compare/v1.3.1...v2.0.0) (2026-04-11)


### Bug Fixes

* update README ([066363a](https://github.com/NoTIPswe/notip-data-consumer/commit/066363ab2a46b2a803ff42115c4aa9eac34a9ff0))


### BREAKING CHANGES

* update version

## [1.3.1](https://github.com/NoTIPswe/notip-data-consumer/compare/v1.3.0...v1.3.1) (2026-04-10)


### Bug Fixes

* nats problems ([7a662b2](https://github.com/NoTIPswe/notip-data-consumer/commit/7a662b2854eaba83c1112315e86d665cb9c68118))
* update alert subject format and increase alert config refresh interval ([8aa0f0b](https://github.com/NoTIPswe/notip-data-consumer/commit/8aa0f0b2319ec4ff29d01d1383479c2484c39bf6))

# [1.3.0](https://github.com/NoTIPswe/notip-data-consumer/compare/v1.2.0...v1.3.0) (2026-04-08)


### Features

* add validation for gateway lifecycle response and enhance tests ([8298600](https://github.com/NoTIPswe/notip-data-consumer/commit/82986001f6aa45dc23c9809a15b92d7cbb69ff6e))

# [1.2.0](https://github.com/NoTIPswe/notip-data-consumer/compare/v1.1.0...v1.2.0) (2026-04-08)


### Bug Fixes

* add logging for alert config cache refresh and fetch errors ([5deb16d](https://github.com/NoTIPswe/notip-data-consumer/commit/5deb16d9fc096d6a8199e0412051618ee676af82))
* improve database SSL configuration handling and tests ([95421f8](https://github.com/NoTIPswe/notip-data-consumer/commit/95421f8f7f7a4b24e216b4039552339e97a39a4e))
* improve error handling and output in scripts ([1e68c6d](https://github.com/NoTIPswe/notip-data-consumer/commit/1e68c6d9af3a2590cc6109354375bbe7488b3a66))
* migration and DSSL key ([7b34d24](https://github.com/NoTIPswe/notip-data-consumer/commit/7b34d24564e88f9461e0028eed3b8f05ad844aea))
* migrations folder in dockerignore ([7968c19](https://github.com/NoTIPswe/notip-data-consumer/commit/7968c1952b0b86a25f00b7f2ee3143686de06af7))


### Features

* add DB_SSL_ROOT_CERT support for SSL verification in database configuration ([ba01e0a](https://github.com/NoTIPswe/notip-data-consumer/commit/ba01e0aa29b78d0d08e9917fb9dd6394ac862844))
* enhance AlertConfigCache with exponential backoff and logging ([7f95932](https://github.com/NoTIPswe/notip-data-consumer/commit/7f95932d200de8c37d68a2ee5bfd3a2dee860a41))

# [1.1.0](https://github.com/NoTIPswe/notip-data-consumer/compare/v1.0.1...v1.1.0) (2026-04-03)


### Features

* added status management with RR to management api ([d2792fe](https://github.com/NoTIPswe/notip-data-consumer/commit/d2792fe2bc1a7824cba31d7f6b6e27cd85dfc92b))
* enhance metrics tracking for telemetry and heartbeat components ([4dee7fd](https://github.com/NoTIPswe/notip-data-consumer/commit/4dee7fd5447ec03848f96704d4560a0da9d07f93))
* enhance NATS consumers and metrics integration tests ([b8c8064](https://github.com/NoTIPswe/notip-data-consumer/commit/b8c8064f03e7d4a2c2397c83cc72bdc495306195))
* refactor test cases to use constants for gateway IDs and endpoints ([a2322c3](https://github.com/NoTIPswe/notip-data-consumer/commit/a2322c335525e2ae85decb7778cc18b2966b3a93))

## [1.0.1](https://github.com/NoTIPswe/notip-data-consumer/compare/v1.0.0...v1.0.1) (2026-03-30)


### Bug Fixes

* enhance NATSTelemetryConsumer message handling and testing ([39fc018](https://github.com/NoTIPswe/notip-data-consumer/commit/39fc018714e70f17f1fd7265a89e634e1d580898))

# 1.0.0 (2026-03-28)


### Bug Fixes

* added minor tests ([1fccd3a](https://github.com/NoTIPswe/notip-data-consumer/commit/1fccd3ab5c85da6f8a261f1fca317c822b04eccf))
* detail.md removed ([f122846](https://github.com/NoTIPswe/notip-data-consumer/commit/f1228460a1e424194bd4b5f626731b7f08aa7284))
* removing from release job the container to make it run with only needed dependencies ([1bbd62d](https://github.com/NoTIPswe/notip-data-consumer/commit/1bbd62d365082c51c4fc93e6f9e22b79b572a433))
* sonarqube fix ([3a149a5](https://github.com/NoTIPswe/notip-data-consumer/commit/3a149a5c71d6c6b16d1bed26f0a17e2670e4a0f8))
* still not working without root ([f577999](https://github.com/NoTIPswe/notip-data-consumer/commit/f5779999d68f60e0ad85048c1bc614d7e37447b6))
* trying make checkout action work again ([ef80e95](https://github.com/NoTIPswe/notip-data-consumer/commit/ef80e9542748357321e0fb4c5e11302ff3fabf65))
* untested lines ([bbafe67](https://github.com/NoTIPswe/notip-data-consumer/commit/bbafe6741c5d20f7639570913a0642ef7e14fc06))
* update leftovers ([2d3f23a](https://github.com/NoTIPswe/notip-data-consumer/commit/2d3f23acba0e8946b30111892465616337fd0089))
* update the readme file ([8c67f86](https://github.com/NoTIPswe/notip-data-consumer/commit/8c67f864fd1ec51142c6cab77914fb817b6aed74))
* updated scripts ([92e6765](https://github.com/NoTIPswe/notip-data-consumer/commit/92e67652f1e965a479d89246cb4899b2998b54db))
* updated service with the new nats-contracts in infra ([c304b78](https://github.com/NoTIPswe/notip-data-consumer/commit/c304b781855871185fc2c7d5223b2fbf734fca46))
* updated workflows ([1019cbf](https://github.com/NoTIPswe/notip-data-consumer/commit/1019cbfcf635d9b0e51a2629947c1d2855c77e94))


### Features

* add .dockerignore, integration tests, health check, and DB_SSL_MODE configuration ([6c1dadb](https://github.com/NoTIPswe/notip-data-consumer/commit/6c1dadbd6fac74b336312fe2d9a4a02fef237062))
* added integration tests throughout go-testcontainers ([501cb66](https://github.com/NoTIPswe/notip-data-consumer/commit/501cb66b641b8724b29df39d39a0866e90d5a58b))
* added some unit tests ([101e6d0](https://github.com/NoTIPswe/notip-data-consumer/commit/101e6d0ecbf5fbb648af3d2e8463918fd86d2c84))
* dd initial project structure with configuration files, health check endpoint, and AsyncAPI filtering scripts ([2c4d36a](https://github.com/NoTIPswe/notip-data-consumer/commit/2c4d36a947a391f7578963eb9a59197eaa03522d))
* developed the first version of the data-consumer service ([d035b12](https://github.com/NoTIPswe/notip-data-consumer/commit/d035b124520e4c733ba27cb76b5949cfd0fb576d))

# 1.0.0 (2026-03-19)


### Bug Fixes

* updated workflows ([1019cbf](https://github.com/NoTIPswe/notip-data-consumer/commit/1019cbfcf635d9b0e51a2629947c1d2855c77e94))


### Features

* dd initial project structure with configuration files, health check endpoint, and AsyncAPI filtering scripts ([2c4d36a](https://github.com/NoTIPswe/notip-data-consumer/commit/2c4d36a947a391f7578963eb9a59197eaa03522d))
