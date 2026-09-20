package domain

// Quality flags. Every flag attached to a point or layer has an entry in
// FlagDoc explaining the evidence the UI should show.
const (
	FlagRetry            = "DUP_RETRY"         // identical retransmission
	FlagConflict         = "DUP_CONFLICT"      // same seq, different payload
	FlagLate             = "LATE_PACKET"       // arrived after a later seq
	FlagPressureReversal = "PRESSURE_REVERSAL" // short-term pressure inversion
	FlagIcing            = "ICING"             // humidity sensor frozen; RH suspect
	FlagIcingManual      = "ICING_MANUAL"      // icing interval marked by human
	FlagGPSGap           = "GPS_GAP"           // short GPS break, position/alt interpolated
	FlagGPSGapMissing    = "GPS_GAP_MISSING"   // long GPS break, alt/pos not derivable
	FlagInstrumentStatus = "INSTRUMENT_STATUS" // non-ok instrument status word
	FlagExactObs         = "EXACT_OBS"         // level equals an observed pressure
	FlagInterp           = "INTERPOLATED"      // interpolated between same-branch points
	FlagManualPhase      = "MANUAL_PHASE"      // branch phase set by human judgment
	FlagTerminate        = "TERMINATED"        // after instrument termination signal
	FlagTempMissing      = "TEMP_MISSING"      // temperature unavailable at this level
)

// FlagDoc maps a flag to a human-readable rule description.
var FlagDoc = map[string]string{
	FlagRetry:            "重复序号且载荷字节一致，判为重试，只保留首次到达的观测。",
	FlagConflict:         "重复序号但载荷不同，判为冲突；冲突点不平均，生成候选轨迹交人工选择。",
	FlagLate:             "该包在更大序号已到达后才到（迟到包）；可派生新版本，但在重新发布前不改变旧层结。",
	FlagPressureReversal: "短时气压反向（上升支出现升压/下降支出现降压的毛刺），该点不作插值端点。",
	FlagIcing:            "算法识别湿度传感器结冰（持续 100%RH 伴随温度 ≤ 0℃），区间内 RH 不参与插值。",
	FlagIcingManual:      "人工圈定的结冰区间，区间内 RH 判为无效。",
	FlagGPSGap:           "GPS 短时断点（≤2 个采样间隔），高度/位置线性插值并标注。",
	FlagGPSGapMissing:    "GPS 长时断点，高度/位置无法在同一分支内恢复，保留缺失。",
	FlagInstrumentStatus: "仪器状态字非正常，观测按状态字降级。",
	FlagExactObs:         "标准层气压与观测值在容差内相等，直接采用观测值。",
	FlagInterp:           "在同一运动分支的两个有效点之间按 ln(p) 线性插值。",
	FlagManualPhase:      "该分支运动属性来自人工判定（上升/漂浮/下降/终止覆盖）。",
	FlagTerminate:        "终止信号之后的点；可保留但不进入标准层。",
	FlagTempMissing:      "该层夹逼点之一温度缺失；温度保留缺失，其他变量仍照常派生。",
}

// Missing reasons for layers that cannot be derived.
const (
	MissingOutOfRange   = "OUT_OF_RANGE"  // level pressure never crossed by this branch
	MissingNoValidPair  = "NO_VALID_PAIR" // no two same-branch valid points bracket the level
	MissingVariableGap  = "VARIABLE_GAP"  // bracketing points exist but the variable is invalid (icing / status)
	MissingGPSLongGap   = "GPS_LONG_GAP"  // altitude/position unavailable across a long GPS break
	MissingExactInvalid = "EXACT_INVALID" // exact-pressure observation exists but value is invalid
	MissingTerminated   = "TERMINATED"    // beyond instrument termination
)

var MissingDoc = map[string]string{
	MissingOutOfRange:   "该运动分支的气压范围未覆盖此标准层（不跨分支外推）。",
	MissingNoValidPair:  "同一运动分支内不存在两个夹住该标准层的有效气压点。",
	MissingVariableGap:  "夹逼点存在，但目标变量在结冰区间或因仪器状态无效，不能插值。",
	MissingGPSLongGap:   "该层位于 GPS 长断点内，高度/位置不可恢复。",
	MissingExactInvalid: "存在等压观测，但该观测值本身无效（缺失/结冰/状态降级）。",
	MissingTerminated:   "仪器已终止，终止后不派生标准层。",
}

// StandardPressures in hPa, high to low (surface to top of atmosphere).
var StandardPressures = []float64{
	1000, 925, 850, 700, 500, 400, 300, 250, 200, 150, 100, 70, 50, 30, 20, 10,
}

const ExactTolerance = 1e-6
