// Fields whose value is a reference into another package: Expr reports
// them as a selector, not as what they refer to.
package config

import "example.test/imports/lib"

mode:    lib.Mode
legend:  lib.#Legend
variant: lib.#AorB
