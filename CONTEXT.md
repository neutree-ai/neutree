# Neutree Monitoring

This context defines the monitoring language Neutree uses to describe endpoint replicas, runtime usage, and accelerator allocation across cluster types.

## Language

**Workload Role**:
A logical serving role used to distinguish inference-serving workload containers from router, system, or helper containers in Neutree metrics.
_Avoid_: Deployment, container name
