package gen

import "math"

// These parameters are carried with the artifact-bound worker result so the
// server can reproduce the two-feature predictions for every kept pose view.
// The configured worker separately validates the artifact's disjoint controls
// and actual feature-model/preprocessing checksums before advertising it.
type outfitFit struct {
	Mean      []float64 `json:"mean"`
	Scale     []float64 `json:"scale"`
	Weights   []float64 `json:"weights"`
	Intercept *float64  `json:"intercept"`
}

func validMultiviewOutput(out lockWorkerOutput, references int) bool {
	n := len(out.Scores)
	if !validFeatureMethod(out.IdentityMethod, lockCCIPMethod) || out.FeatureMethods["ccip"] != out.IdentityMethod || !validFeatureMethod(out.FeatureMethods["fashion"], "fashion-clip-v1") || references < 0 || references > 5 || out.OutfitReferenceCount != references || len(out.OutfitCosines) != n || len(out.OutfitReferenceIndices) != n || len(out.OutfitPairFeatures) != n || len(out.OutfitPairScores) != n || len(out.OutfitPairReferenceIndices) != n || !validOutfitFit(out.OutfitFit) {
		return false
	}
	for i, values := range out.OutfitCosines {
		selected, pairSelected := out.OutfitReferenceIndices[i], out.OutfitPairReferenceIndices[i]
		features, predictions := out.OutfitPairFeatures[i], out.OutfitPairScores[i]
		if out.Outfits[i] == nil {
			if len(values) != 0 || selected != nil || len(features) != 0 || len(predictions) != 0 || pairSelected != nil {
				return false
			}
			continue
		}
		if len(values) != references || references == 0 || len(features) != references || len(predictions) != references || selected == nil || *selected < 0 || *selected >= references || pairSelected == nil || *pairSelected < 0 || *pairSelected >= references {
			return false
		}
		best, pairBest := math.Inf(-1), math.Inf(-1)
		for j, value := range values {
			if !finiteCosine(value) || len(features[j]) != 2 || !finiteCosine(features[j][0]) || !finiteCosine(features[j][1]) || math.Abs(features[j][0]-value) > 1e-8 || !finiteScore(predictions[j]) {
				return false
			}
			logit := *out.OutfitFit.Intercept
			for k, feature := range features[j] {
				logit += out.OutfitFit.Weights[k] * (feature - out.OutfitFit.Mean[k]) / out.OutfitFit.Scale[k]
			}
			prediction := 1 / (1 + math.Exp(-max(-60.0, min(60.0, logit))))
			if math.IsNaN(logit) || math.Abs(prediction-predictions[j]) > 1e-8 {
				return false
			}
			best, pairBest = max(best, value), max(pairBest, predictions[j])
		}
		if math.Abs(values[*selected]-best) > 1e-8 || math.Abs(predictions[*pairSelected]-pairBest) > 1e-8 || math.Abs(*out.Outfits[i]-pairBest) > 1e-8 {
			return false
		}
	}
	return true
}

func finiteCosine(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= -1.00001 && n <= 1.00001
}

func validOutfitFit(f *outfitFit) bool {
	if f == nil || f.Intercept == nil || len(f.Mean) != 2 || len(f.Scale) != 2 || len(f.Weights) != 2 {
		return false
	}
	for _, row := range [][]float64{f.Mean, f.Scale, f.Weights, {*f.Intercept}} {
		for _, v := range row {
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 10000 {
				return false
			}
		}
	}
	return f.Scale[0] > 0 && f.Scale[1] > 0
}
