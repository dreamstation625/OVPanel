package store

import "time"

// nowUTC 单独抽取，便于测试替换时间来源。
var nowUTC = func() time.Time { return time.Now().UTC() }
