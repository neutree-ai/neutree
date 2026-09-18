import unittest

from verify import samples


class SamplesTest(unittest.TestCase):
    def test_selects_exact_endpoint_model_and_preserves_target_identity(self):
        text = '''# TYPE count counter
count{endpoint="/ee",virtual_model="v",upstream="a",upstream_model="real"} 3
count{endpoint="/other",virtual_model="v",upstream="a",upstream_model="real"} 80
count{endpoint="/ee",virtual_model="other",upstream="a",upstream_model="real"} 90
count{endpoint="/ee",virtual_model="v",upstream="a",upstream_model="another"} 4
'''
        self.assertEqual(samples(text, 'count', '/ee', 'v'), {('a', 'real'): 3, ('a', 'another'): 4})

    def test_decodes_escaped_prometheus_labels(self):
        text = r'count{endpoint="/ee",virtual_model="v",upstream="a\"b",upstream_model="real"} 2'
        self.assertEqual(samples(text, 'count', '/ee', 'v'), {('a"b', 'real'): 2})

    def test_does_not_accept_a_different_metric_name(self):
        text = 'count_extra{endpoint="/ee",virtual_model="v",upstream="a",upstream_model="real"} 2'
        self.assertEqual(samples(text, 'count', '/ee', 'v'), {})


if __name__ == '__main__':
    unittest.main()
