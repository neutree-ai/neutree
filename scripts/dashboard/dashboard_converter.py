#!/usr/bin/env python3
"""
Generic Grafana Dashboard Conversion Tool

Supports automated Dashboard conversion by defining different conversion rules
through configuration files.
"""

import json
import re
import sys
from pathlib import Path
from typing import Dict, List, Callable, Any, Optional
from dataclasses import dataclass, field


@dataclass
class ConversionRule:
    """Conversion rule definition"""
    name: str
    description: str
    pattern: str
    replacement: str
    is_regex: bool = True


@dataclass
class VariableTemplate:
    """Template variable definition"""
    name: str
    label: str
    query: str
    multi: bool = False
    include_all: bool = False
    all_value: str = ".*"
    variable_type: str = "query"
    hide: int = 0
    refresh: int = 2
    current: Dict[str, Any] = field(default_factory=dict)
    datasource: Dict[str, str] = field(default_factory=lambda: {"type": "prometheus", "uid": "${DS_PROMETHEUS}"})


@dataclass
class VariableOverride:
    """Replace selected fields of an existing template variable, matched by name.

    Conversion rules only reach query fields, so a variable description carried
    over from upstream keeps naming upstream metrics and labels. Matching by name
    instead of by pattern means the replacement lands no matter how upstream
    rewords the original text.
    """
    name: str
    description: Optional[str] = None


@dataclass
class DashboardConversionConfig:
    """Dashboard conversion configuration"""
    name: str
    description: str
    source_file: str
    output_file: str
    uid: str = None  # If None, keep original UID

    # Conversion rules
    metric_rules: List[ConversionRule] = field(default_factory=list)
    filter_rules: List[ConversionRule] = field(default_factory=list)
    custom_rules: List[ConversionRule] = field(default_factory=list)

    # Template variables
    variables: List[VariableTemplate] = field(default_factory=list)
    keep_datasource_variable: bool = True
    variable_overrides: List[VariableOverride] = field(default_factory=list)


class DashboardConverter:
    """Dashboard converter"""

    def __init__(self, config: DashboardConversionConfig):
        self.config = config

    def apply_rules(self, text: str, rules: List[ConversionRule]) -> str:
        """Apply conversion rules to text"""
        for rule in rules:
            if rule.is_regex:
                text = re.sub(rule.pattern, rule.replacement, text)
            else:
                text = text.replace(rule.pattern, rule.replacement)
        return text

    def convert_expression(self, expr: str) -> str:
        """Convert query expression"""
        # Apply rules in order: metrics -> filters -> custom
        expr = self.apply_rules(expr, self.config.metric_rules)
        expr = self.apply_rules(expr, self.config.filter_rules)
        expr = self.apply_rules(expr, self.config.custom_rules)
        return expr

    def convert_target(self, target: dict) -> dict:
        """Convert a single target"""
        if 'expr' in target and isinstance(target['expr'], str):
            target['expr'] = self.convert_expression(target['expr'])
        return target

    def convert_panel(self, panel: dict) -> dict:
        """Convert a single panel"""
        if 'targets' in panel:
            panel['targets'] = [self.convert_target(t) for t in panel['targets']]
        return panel

    def convert_variable(self, variable: dict) -> dict:
        """Convert a single template variable.

        Only the query-bearing fields are rewritten: a rule that renames a metric
        or a label would corrupt a description, where the same words are prose.
        """
        if isinstance(variable.get('definition'), str):
            variable['definition'] = self.convert_expression(variable['definition'])

        query = variable.get('query')
        if isinstance(query, str):
            variable['query'] = self.convert_expression(query)
        elif isinstance(query, dict) and isinstance(query.get('query'), str):
            query['query'] = self.convert_expression(query['query'])

        return variable

    def apply_variable_overrides(self, dashboard: dict) -> None:
        """Apply per-name overrides to the dashboard's template variables"""
        if not self.config.variable_overrides:
            return

        templating = dashboard.get('templating')
        variables = templating.get('list') if isinstance(templating, dict) else None
        if not isinstance(variables, list):
            return

        overrides = {override.name: override for override in self.config.variable_overrides}

        for variable in variables:
            override = overrides.get(variable.get('name'))
            if override is None:
                continue

            if override.description is not None:
                variable['description'] = override.description

    def create_variable(self, template: VariableTemplate) -> dict:
        """Create variable configuration from template"""
        var = {
            "name": template.name,
            "label": template.label,
            "type": template.variable_type,
            "hide": template.hide,
            "refresh": template.refresh,
            "datasource": template.datasource,
        }

        if template.variable_type == "query":
            var["definition"] = template.query
            var["query"] = {
                "query": template.query,
                "refId": f"Var-{template.name[:3]}"
            }
            var["multi"] = template.multi
            var["includeAll"] = template.include_all

            if template.include_all:
                var["allValue"] = template.all_value

            # Set default current value
            if template.current:
                var["current"] = template.current
            elif template.include_all:
                var["current"] = {
                    "selected": True,
                    "text": ["All"],
                    "value": ["$__all"]
                }
            else:
                var["current"] = {}

            var["options"] = []
            var["regex"] = ""
            var["sort"] = 0

        return var

    def create_variables(self) -> List[dict]:
        """Create all variables"""
        variables = []

        # If keeping datasource variable, add it at the beginning
        if self.config.keep_datasource_variable:
            variables.append({
                "current": {
                    "selected": False,
                    "text": "prometheus",
                    "value": "edx8memhpd9tsa"
                },
                "hide": 0,
                "includeAll": False,
                "label": "datasource",
                "multi": False,
                "name": "DS_PROMETHEUS",
                "options": [],
                "query": "prometheus",
                "queryValue": "",
                "refresh": 1,
                "regex": "",
                "skipUrlSync": False,
                "type": "datasource"
            })

        # Add configured variables
        for template in self.config.variables:
            variables.append(self.create_variable(template))

        return variables

    def convert_dashboard(self, source_dashboard: dict) -> dict:
        """Convert entire dashboard"""
        # Deep copy to avoid modifying original data
        dashboard = json.loads(json.dumps(source_dashboard))

        # Convert all panels
        if 'panels' in dashboard:
            dashboard['panels'] = [self.convert_panel(p) for p in dashboard['panels']]

        # Replace template variables. Which branch runs is load-bearing: a config
        # with neither `variables` nor `keep_datasource_variable` carries the
        # upstream variables over, and they therefore need converting below.
        # Adding a single VariableTemplate flips this to the replace branch, which
        # discards the upstream variables wholesale — including a $Cluster that
        # panel expressions still reference.
        if self.config.variables or self.config.keep_datasource_variable:
            if 'templating' not in dashboard:
                dashboard['templating'] = {}
            dashboard['templating']['list'] = self.create_variables()
        else:
            # Upstream variables are carried over untouched, so they still name
            # upstream metrics and labels. Run them through the same rules.
            templating = dashboard.get('templating')
            if isinstance(templating, dict) and isinstance(templating.get('list'), list):
                templating['list'] = [self.convert_variable(v) for v in templating['list']]

        # Overrides run last so they reach both carried-over and generated variables.
        self.apply_variable_overrides(dashboard)

        # Update UID (if specified)
        if self.config.uid:
            dashboard['uid'] = self.config.uid

        return dashboard

    def convert(self) -> bool:
        """Execute conversion"""
        source_file = self.config.source_file
        output_file = self.config.output_file

        print(f"📖 Reading source file: {source_file}")
        with open(source_file, 'r', encoding='utf-8') as f:
            source_dashboard = json.load(f)

        print(f"🔄 Starting conversion: {self.config.name}")
        print(f"   {self.config.description}")
        converted_dashboard = self.convert_dashboard(source_dashboard)

        print(f"💾 Writing output file: {output_file}")
        with open(output_file, 'w', encoding='utf-8') as f:
            json.dump(converted_dashboard, f, indent=2, ensure_ascii=False)

        print("✅ Conversion complete!")
        print(f"\n📊 Conversion summary:")
        print(f"  - Config: {self.config.name}")
        print(f"  - Source file: {source_file}")
        print(f"  - Output file: {output_file}")
        print(f"  - Panel count: {len(converted_dashboard.get('panels', []))}")
        print(f"  - Variable count: {len(converted_dashboard.get('templating', {}).get('list', []))}")
        print(f"  - Metric rules: {len(self.config.metric_rules)}")
        print(f"  - Filter rules: {len(self.config.filter_rules)}")
        print(f"  - Custom rules: {len(self.config.custom_rules)}")

        return True


def load_config_from_file(config_file: str) -> DashboardConversionConfig:
    """Load configuration from JSON file"""
    with open(config_file, 'r', encoding='utf-8') as f:
        data = json.load(f)

    # Parse conversion rules
    metric_rules = [
        ConversionRule(**rule) for rule in data.get('metric_rules', [])
    ]
    filter_rules = [
        ConversionRule(**rule) for rule in data.get('filter_rules', [])
    ]
    custom_rules = [
        ConversionRule(**rule) for rule in data.get('custom_rules', [])
    ]

    # Parse variable templates
    variables = [
        VariableTemplate(**var) for var in data.get('variables', [])
    ]

    variable_overrides = [
        VariableOverride(**override) for override in data.get('variable_overrides', [])
    ]

    return DashboardConversionConfig(
        name=data['name'],
        description=data['description'],
        source_file=data['source_file'],
        output_file=data['output_file'],
        uid=data.get('uid'),
        metric_rules=metric_rules,
        filter_rules=filter_rules,
        custom_rules=custom_rules,
        variables=variables,
        keep_datasource_variable=data.get('keep_datasource_variable', True),
        variable_overrides=variable_overrides
    )


def main():
    """Main function"""
    if len(sys.argv) < 2:
        print("Usage: python dashboard_converter.py <config_file>")
        print("Example: python dashboard_converter.py configs/vllm_to_ray.json")
        sys.exit(1)

    config_file = sys.argv[1]

    if not Path(config_file).exists():
        print(f"❌ Error: Configuration file not found: {config_file}", file=sys.stderr)
        sys.exit(1)

    try:
        config = load_config_from_file(config_file)
        converter = DashboardConverter(config)
        success = converter.convert()
        sys.exit(0 if success else 1)
    except Exception as e:
        print(f"❌ Conversion failed: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == '__main__':
    main()
