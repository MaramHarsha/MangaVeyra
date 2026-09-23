// Package setup is the first-run wizard (G8): 1 admin account, 2 instance
// name, public URL, mode and sign-up (telemetry is shown as off), 3 connect
// models (GPU / worker detection from the workers table, provider cards and
// Local only mode; the AI module fills the ModelStep hook), 4 check & finish.
//
// Until an admin exists every page redirects here (core.SetupGate); the admin
// step only works while no admin exists. Screens: /setup/{admin|instance|models|finish}.
package setup
