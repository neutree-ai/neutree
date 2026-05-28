from __future__ import annotations

from typing import List

from router.scheduling.plugins import (
    ConsistentHashWithBoundedLoadScorer,
    PDRoleFilter,
    PDSameRoleGroupFilter,
    ReadyEndpointFilter,
    WeightedEndpointScorer,
)
from router.scheduling.profiles import SchedulingProfile
from router.scheduling.types import EndpointInfo, EndpointRouteDecision, SchedulingContext


class PDSameHostProfileHandler:
    def __init__(
        self,
        virtual_nodes_per_replica: int = 100,
        load_factor: float = 1.25,
        max_user_messages_for_cache: int = 2,
    ):
        scorers = [
            WeightedEndpointScorer(
                ConsistentHashWithBoundedLoadScorer(
                    virtual_nodes_per_replica,
                    load_factor,
                    max_user_messages_for_cache,
                )
            ),
        ]
        self._decode_profile = SchedulingProfile(
            [ReadyEndpointFilter(), PDRoleFilter("decode")],
            scorers,
            name="decode",
        )
        self._prefill_profile = SchedulingProfile(
            [ReadyEndpointFilter(), PDRoleFilter("prefill"), PDSameRoleGroupFilter()],
            scorers,
            name="prefill",
        )

    def pick(
        self,
        endpoints: List[EndpointInfo],
        context: SchedulingContext,
    ) -> EndpointRouteDecision:
        try:
            decode = self._decode_profile.pick(endpoints, context)
        except ValueError as exc:
            raise ValueError("no ready decode endpoints available") from exc

        try:
            prefill = self._prefill_profile.pick(endpoints, context.with_selected_endpoint(decode))
        except ValueError as exc:
            raise ValueError(
                f"no ready prefill endpoint in role group {decode.pd_role_group_id}"
            ) from exc
        return EndpointRouteDecision(endpoint=decode, prefill=prefill, decode=decode)


PDSameHostPicker = PDSameHostProfileHandler
