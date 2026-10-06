.PHONY: check-release package-release
check-release:
	sh scripts/check-release.sh

package-release:
	sh scripts/package-release.sh
