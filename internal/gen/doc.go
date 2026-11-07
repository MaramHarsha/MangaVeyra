// Package gen plans and executes durable generation jobs.
//
// Gate A implements provider-to-persisted script generation. Image, page,
// variation, region and character-sheet operations return ErrDeferredFeature
// until their later product gates own the required persistence paths.
package gen
