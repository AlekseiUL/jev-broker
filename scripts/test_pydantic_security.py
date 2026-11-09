"""Synthetic regression controls for the offline MCP test dependencies.

No provider calls. These exercise upstream advisory shapes, not a demonstrated
attack path through the Go broker.
"""
from typing import Annotated
import unittest

from pydantic import BaseModel, ConfigDict, Field, TypeAdapter, ValidationError
from pydantic.dataclasses import dataclass
from typing_extensions import TypedDict


class ExtraKeys(BaseModel):
    model_config = ConfigDict(extra="allow")
    __pydantic_extra__: dict[Annotated[str, Field(pattern=r"^x_")], int]


class AliasedDict(TypedDict):
    __pydantic_config__ = ConfigDict(extra="allow")
    count: Annotated[int, Field(alias="n")]


@dataclass(config=ConfigDict(extra="allow"))
class AliasedData:
    count: int = Field(alias="n")


class AliasedModel(BaseModel):
    model_config = ConfigDict(extra="allow")
    count: int = Field(alias="n")


class PydanticSecurityTests(unittest.TestCase):
    def test_extra_json_key_rejected(self):
        # GHSA-qpmf-fgh3-ff25: JSON must validate extra key constraints.
        with self.assertRaises(ValidationError):
            ExtraKeys.model_validate_json('{"bad": 1}')

    def test_extra_python_key_rejected(self):
        # Unaffected-path control: Python validation already rejected this.
        with self.assertRaises(ValidationError):
            ExtraKeys.model_validate({"bad": 1})

    def test_valid_extra_keys_and_values(self):
        for value in (ExtraKeys.model_validate({"x_good": "7"}),
                      ExtraKeys.model_validate_json('{"x_good": "7"}')):
            self.assertEqual(value.__pydantic_extra__, {"x_good": 7})

    def test_invalid_extra_value_rejected(self):
        with self.assertRaises(ValidationError):
            ExtraKeys.model_validate_json('{"x_good": "not-an-int"}')

    def test_typed_dict_collision_cannot_overwrite(self):
        # GHSA-3r8g-cqwr-jr29: name must not overwrite validated alias.
        adapter = TypeAdapter(AliasedDict)
        for validate, payload in (
            (adapter.validate_python, {"n": 7, "count": "unvalidated"}),
            (adapter.validate_json, '{"n": 7, "count": "unvalidated"}'),
        ):
            with self.subTest(mode=validate.__name__):
                value = validate(payload)
                self.assertEqual(value["count"], 7)
                self.assertIsInstance(value["count"], int)

    def test_dataclass_collision_cannot_overwrite(self):
        adapter = TypeAdapter(AliasedData)
        for validate, payload in (
            (adapter.validate_python, {"n": 7, "count": "unvalidated"}),
            (adapter.validate_json, '{"n": 7, "count": "unvalidated"}'),
        ):
            with self.subTest(mode=validate.__name__):
                value = validate(payload)
                self.assertEqual(value.count, 7)
                self.assertIsInstance(value.count, int)

    def test_valid_alias_and_unrelated_extra(self):
        payload = {"n": "7", "other": "allowed"}
        self.assertEqual(TypeAdapter(AliasedDict).validate_python(payload),
                         {"count": 7, "other": "allowed"})
        value = TypeAdapter(AliasedData).validate_python(payload)
        self.assertEqual(value.count, 7)
        self.assertEqual(value.other, "allowed")

    def test_invalid_alias_rejected(self):
        for schema in (AliasedDict, AliasedData):
            with self.subTest(schema=schema.__name__):
                with self.assertRaises(ValidationError):
                    TypeAdapter(schema).validate_python({"n": "bad"})

    def test_basemodel_unaffected_control(self):
        value = AliasedModel.model_validate({"n": 7, "count": "extra"})
        self.assertEqual(value.count, 7)
        self.assertEqual(value.__pydantic_extra__, {"count": "extra"})


if __name__ == "__main__":
    unittest.main()
