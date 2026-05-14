# Phase 0 Demo — naive PD ingress.
#
# The MVP (PR-ingress-lib) will add module-level _SHARED state, ObserverRouter,
# candidate_builder, and CHWBL scheduling. For Demo this package only exports
# pd_ingress.PDIngress which dispatches every request to the single
# PDCollocatedBackend handle bound at app_builder time.
