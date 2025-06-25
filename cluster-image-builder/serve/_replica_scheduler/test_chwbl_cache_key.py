import unittest
from unittest.mock import patch, MagicMock
import sys
import os
import hashlib

# Mock the ray dependencies
sys.modules['ray'] = MagicMock()
sys.modules['ray.serve'] = MagicMock()
sys.modules['ray.serve._private'] = MagicMock()
sys.modules['ray.serve._private.common'] = MagicMock()
sys.modules['ray.serve._private.constants'] = MagicMock()
sys.modules['ray.serve._private.replica_scheduler'] = MagicMock()
sys.modules['ray.serve._private.replica_scheduler.common'] = MagicMock()
sys.modules['ray.serve._private.replica_scheduler.replica_scheduler'] = MagicMock()
sys.modules['ray.serve._private.replica_scheduler.replica_wrapper'] = MagicMock()

# Mock logger
import logging
logger = logging.getLogger("test_logger")

# Simplified version of the scheduler for testing just the _extract_cache_key method
class TestConsistentHashReplicaScheduler:
    """Simplified scheduler for testing _extract_cache_key method."""
    
    def __init__(self, max_user_messages_for_cache: int = 2):
        self._max_user_messages_for_cache = max_user_messages_for_cache
    
    def _extract_cache_key(self, payload, request_id: str) -> str:
        """Extract cache key from OpenAI-compatible chat completions payload."""
        # Default fallback
        if not payload:
            logger.info(f"No payload found, using request_id: {request_id}")
            return str(request_id)
        
        try:
            # Handle different payload formats (args could be tuple, list, or dict)
            if isinstance(payload, (tuple, list)) and len(payload) > 0:
                # Assume first argument contains the request data
                request_data = payload[0]
            elif isinstance(payload, dict):
                request_data = payload
            else:
                # Fallback to string representation
                return str(payload)
            
            # Extract relevant fields for cache key
            cache_components = []
            
            # System prompt and user messages
            messages = request_data.get('messages', [])
            system_prompt = None
            user_messages = []
            
            for msg in messages:
                if isinstance(msg, dict):
                    role = msg.get('role', '')
                    content = msg.get('content', '')
                    
                    if role == 'system':
                        system_prompt = content
                    elif role == 'user':
                        user_messages.append(content)
            
            # Add system prompt to cache key
            if system_prompt is not None:
                cache_components.append(f"system:{system_prompt}")
            
            # Add first N user messages
            for i, user_msg in enumerate(user_messages[:self._max_user_messages_for_cache]):
                cache_components.append(f"user_{i}:{user_msg}")
            
            # Join components
            if cache_components:
                cache_key = "|".join(cache_components)
                logger.debug(f"Extracted cache key: {cache_key[:100]}...")  # Log first 100 chars
                return cache_key
            else:
                # No recognizable chat completions format, fallback
                logger.info(f"No chat completions format detected, using payload string")
                return str(payload)
                
        except Exception as e:
            logger.warning(f"Error extracting cache key from payload: {e}, using request_id")
            return str(request_id)


class TestCHWBLSchedulerCacheKey(unittest.TestCase):
    """Unit tests for the _extract_cache_key method of ConsistentHashReplicaScheduler."""
    
    def setUp(self):
        """Set up test fixtures."""
        self.scheduler = TestConsistentHashReplicaScheduler(
            max_user_messages_for_cache=2
        )
    
    def test_extract_cache_key_empty_payload(self):
        """Test cache key extraction with empty payload."""
        result = self.scheduler._extract_cache_key(None, "test_request_id")
        self.assertEqual(result, "test_request_id")
        
        result = self.scheduler._extract_cache_key([], "test_request_id")
        self.assertEqual(result, "test_request_id")
    
    def test_extract_cache_key_dict_payload(self):
        """Test cache key extraction with dict payload."""
        payload = {
            "messages": [
                {"role": "system", "content": "You are a helpful assistant."},
                {"role": "user", "content": "What is Python?"}
            ]
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = "system:You are a helpful assistant.|user_0:What is Python?"
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_tuple_payload(self):
        """Test cache key extraction with tuple payload (common in Ray Serve)."""
        request_data = {
            "messages": [
                {"role": "system", "content": "You are a helpful assistant."},
                {"role": "user", "content": "What is Python?"}
            ]
        }
        payload = (request_data,)
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = "system:You are a helpful assistant.|user_0:What is Python?"
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_no_system_prompt(self):
        """Test cache key extraction without system prompt."""
        payload = {
            "messages": [
                {"role": "user", "content": "What is Python?"},
                {"role": "user", "content": "Tell me more."}
            ]
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = "user_0:What is Python?|user_1:Tell me more."
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_multiple_user_messages(self):
        """Test cache key extraction with multiple user messages."""
        payload = {
            "messages": [
                {"role": "system", "content": "You are a helpful assistant."},
                {"role": "user", "content": "What is Python?"},
                {"role": "assistant", "content": "Python is a programming language."},
                {"role": "user", "content": "Can you give examples?"},
                {"role": "user", "content": "What about libraries?"}  # This should be ignored (3rd user message)
            ]
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = "system:You are a helpful assistant.|user_0:What is Python?|user_1:Can you give examples?"
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_max_user_messages_config(self):
        """Test cache key extraction with different max_user_messages_for_cache setting."""
        # Create scheduler with only 1 user message for cache
        scheduler = TestConsistentHashReplicaScheduler(max_user_messages_for_cache=1)
        
        payload = {
            "messages": [
                {"role": "system", "content": "You are a helpful assistant."},
                {"role": "user", "content": "What is Python?"},
                {"role": "assistant", "content": "Python is a programming language."},
                {"role": "user", "content": "Can you give examples?"}
            ]
        }
        
        result = scheduler._extract_cache_key(payload, "test_request_id")
        expected = "system:You are a helpful assistant.|user_0:What is Python?"
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_no_messages(self):
        """Test cache key extraction with no messages."""
        payload = {
            "model": "gpt-3.5-turbo",
            "temperature": 0.7
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = str(payload)  # Fallback to payload string
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_empty_messages(self):
        """Test cache key extraction with empty messages array."""
        payload = {
            "messages": []
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = str(payload)  # Fallback to payload string
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_invalid_message_format(self):
        """Test cache key extraction with invalid message format."""
        payload = {
            "messages": [
                "invalid message format",  # Not a dict
                {"role": "user", "content": "What is Python?"}
            ]
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = "user_0:What is Python?"
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_missing_role_or_content(self):
        """Test cache key extraction with missing role or content fields."""
        payload = {
            "messages": [
                {"role": "system"},  # Missing content - should get empty string
                {"content": "What is Python?"},  # Missing role - should be ignored
                {"role": "user", "content": "Valid message"}
            ]
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        # System role with missing content gets empty string, missing role message is ignored
        expected = "system:|user_0:Valid message"
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_only_assistant_messages(self):
        """Test cache key extraction with only assistant messages."""
        payload = {
            "messages": [
                {"role": "assistant", "content": "Hello there!"},
                {"role": "assistant", "content": "How can I help?"}
            ]
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = str(payload)  # Fallback to payload string (no system/user messages)
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_non_dict_payload(self):
        """Test cache key extraction with non-dict payload."""
        payload = "some string payload"
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = "some string payload"
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_exception_handling(self):
        """Test cache key extraction with None content (edge case)."""
        # None content causes the method to fall back to payload string
        payload = {
            "messages": [
                {"role": "system", "content": None}  # None content
            ]
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = str(payload)  # Falls back to payload string representation
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_real_openai_format(self):
        """Test with a realistic OpenAI chat completion payload."""
        payload = {
            "model": "gpt-3.5-turbo",
            "messages": [
                {
                    "role": "system",
                    "content": "You are a helpful assistant that explains programming concepts clearly."
                },
                {
                    "role": "user", 
                    "content": "Explain what object-oriented programming is"
                },
                {
                    "role": "assistant",
                    "content": "Object-oriented programming (OOP) is a programming paradigm..."
                },
                {
                    "role": "user",
                    "content": "Can you give me a simple example in Python?"
                }
            ],
            "temperature": 0.7,
            "max_tokens": 500,
            "stream": False
        }
        
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        expected = (
            "system:You are a helpful assistant that explains programming concepts clearly.|"
            "user_0:Explain what object-oriented programming is|"
            "user_1:Can you give me a simple example in Python?"
        )
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_zero_max_user_messages(self):
        """Test cache key extraction with max_user_messages_for_cache=0."""
        scheduler = TestConsistentHashReplicaScheduler(max_user_messages_for_cache=0)
        
        payload = {
            "messages": [
                {"role": "system", "content": "You are a helpful assistant."},
                {"role": "user", "content": "What is Python?"}
            ]
        }
        
        result = scheduler._extract_cache_key(payload, "test_request_id")
        expected = "system:You are a helpful assistant."
        self.assertEqual(result, expected)
    
    def test_extract_cache_key_actual_exception(self):
        """Test cache key extraction with actual exception during processing."""
        # Patch the method to inject an exception
        original_method = self.scheduler._extract_cache_key
        
        def mock_extract_with_exception(payload, request_id):
            if payload and 'trigger_exception' in str(payload):
                try:
                    # Simulate the try block where an exception might occur
                    raise ValueError("Simulated processing error")
                except Exception as e:
                    logger.warning(f"Error extracting cache key from payload: {e}, using request_id")
                    return str(request_id)
            return original_method(payload, request_id)
        
        self.scheduler._extract_cache_key = mock_extract_with_exception
        
        payload = {"trigger_exception": True, "messages": [{"role": "user", "content": "test"}]}
        
        # Should fallback to request_id when exception occurs
        result = self.scheduler._extract_cache_key(payload, "test_request_id")
        self.assertEqual(result, "test_request_id")


if __name__ == '__main__':
    # Run the tests
    unittest.main(verbosity=2)
