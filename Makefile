
.PHONY: update-base-images-versions
update-base-images-versions:
	##~ Options: version=vMAJOR.MINOR.PATCH
	curl --fail -sSLO https://fox.flant.com/api/v4/projects/deckhouse%2Fbase-images/packages/generic/base_images/$(version)/base_images.yml
