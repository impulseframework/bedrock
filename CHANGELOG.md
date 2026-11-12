# Changelog

## [0.1.2](https://github.com/impulseframework/bedrock/compare/v0.1.1...v0.1.2) (2026-11-12)


### Features

* a root organization turns immutable releases on for every repository of its GitHub organization, as the organization's own policy, which the apply sets as the infrastructure app and never narrows or turns off, so a published release keeps its tag and its assets; a plan and a check name the change while the policy names no repository or selected ones, and a token that cannot read the policy is noted as not checked ([#10](https://github.com/impulseframework/bedrock/issues/10)) ([2141261](https://github.com/impulseframework/bedrock/commit/214126138296d7c1d0b6581b1148ab72405ece97))
* a site answers the framework's welcome page from its registration until its first release, over HTTP/1.1 and HTTP/2 alike: a first placement names the welcome image this repository publishes (ghcr.io/impulseframework/welcome:v1) where it named Cloud Run's sample, which answered 502 at a site on the framework's server ([#6](https://github.com/impulseframework/bedrock/issues/6)) ([78923fd](https://github.com/impulseframework/bedrock/commit/78923fd96dcc08041edc9a0a8498aac089573336))


### Bug Fixes

* a first placement names the welcome image by its digest, not its tag, so a service is created with the image that was published and with nothing the tag is moved to later; the workflow that publishes the image holds that digest to the registry at every run ([#9](https://github.com/impulseframework/bedrock/issues/9)) ([dbc7d4f](https://github.com/impulseframework/bedrock/commit/dbc7d4fab33763b97baedfd2d72525e950699b50))
* bedrock upgrade in a root organization's repository applies no 0-bootstrap, a layer a root organization does not have: where the pin changes kind it says what the move does to the two triggers, asks for nothing, and after a move from a release to a commit names what brings them to the commit shape, bedrock org bootstrap again in the clone ([#6](https://github.com/impulseframework/bedrock/issues/6)) ([78923fd](https://github.com/impulseframework/bedrock/commit/78923fd96dcc08041edc9a0a8498aac089573336))


### Code Upgrade

* **deps:** bump the go-dependencies group across 1 directory with 6 updates ([#8](https://github.com/impulseframework/bedrock/issues/8)) ([f657999](https://github.com/impulseframework/bedrock/commit/f657999e4beb3c55bc9f83f30113d7344b967561))
* **deps:** Go 1.27.4; the Go image moves to go1.27.4, so built images carry net/http GO-2026-6859's fix ([#6](https://github.com/impulseframework/bedrock/issues/6)) ([78923fd](https://github.com/impulseframework/bedrock/commit/78923fd96dcc08041edc9a0a8498aac089573336))

## [0.1.1](https://github.com/impulseframework/bedrock/compare/v0.1.0...v0.1.1) (2026-11-10)


### Bug Fixes

* bedrock upgrade's help names bedrock's own repository as the home of a commit pin ([#4](https://github.com/impulseframework/bedrock/issues/4)) ([3681ef8](https://github.com/impulseframework/bedrock/commit/3681ef83583735535d30874be8ac9d8425c7865e))

## 0.1.0 (2026-11-10)


### Features

* bedrock, the deployment tool for applications built on the Impulse Framework ([#1](https://github.com/impulseframework/bedrock/issues/1)) ([f7764d3](https://github.com/impulseframework/bedrock/commit/f7764d3a6d459851662de2c40e8eb413fe884fb8))
