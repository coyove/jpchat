module github.com/coyove/jpchat

go 1.20

require (
	github.com/chai2010/webp v1.1.1
	github.com/coyove/bbolt v1.3.9-0.20240227033235-c2dac416ece3
	github.com/coyove/sdss v0.0.0-20231129015646-c2ec58cca6a2
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646
	github.com/sirupsen/logrus v1.9.3
	golang.org/x/image v0.15.0
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
)

require (
	golang.org/x/sys v0.5.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)

replace github.com/chai2010/webp v1.1.1 => ./webp
