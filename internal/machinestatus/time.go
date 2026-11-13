package machinestatus

import "time"

func unixTime() float64 { return float64(time.Now().Unix()) }
