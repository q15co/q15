package config

import (
	// Embed the IANA timezone database so container TZ names work in slim runtimes.
	_ "time/tzdata"
)
