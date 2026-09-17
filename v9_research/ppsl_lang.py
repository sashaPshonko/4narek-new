#!/usr/bin/env python3
"""
PPSL — Pricing Policy Structure Language (research).

Typed, depth-limited DSL so GP can invent structure, not only Chrom weights.
Programs map Features → (action, target_price_hint).

Hard safety (floor, max jump) is applied OUTSIDE the program by the simulator.
Does NOT change production Go.
"""
from __future__ import annotations

import random
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional, Tuple, Union

# ── Feature vector (only what fidelity sim can honestly supply) ─────────

FEATURE_NAMES = [
    "price",
    "p10",
    "ratio",  # price/p10 (None→nan handling via present)
    "gap",  # p10 - price
    "gap_frac",  # (p10-price)/p10
    "held",
    "share",
    "fill",  # held/share
    "sales",
    "buys",
    "vel",  # sales - buys
    "demand",  # sales / max(buys, 0.5)
    "book_n",
    "depth_ok",  # 1 if depth in {ok,thick}
    "depth_thick",
    "night",
    "empty_streak",
    "step",
    "nac",
    "try_sells",
    "seller_n",  # unique sellers in book window (0 if missing)
    "up_cd",
    "down_cd",
    "up_streak",
]


@dataclass
class Features:
    vals: Dict[str, float]

    def get(self, name: str) -> float:
        v = self.vals.get(name)
        if v is None:
            return 0.0
        return float(v)


def features_from_obs(obs: dict, st, nac: float) -> Features:
    price = float(st.price)
    p10 = obs.get("mkt") or obs.get("p10")
    p10f = float(p10) if p10 else 0.0
    share = float(obs.get("share") or 12)
    held = float(st.held)
    sales = float(obs.get("sales") or 0)
    buys = float(obs.get("buys") or 0)
    step = float(obs.get("step") or 100_000)
    depth = obs.get("depth") or "thin"
    ratio = (price / p10f) if p10f > 0 else 0.0
    gap = (p10f - price) if p10f > 0 else 0.0
    return Features(
        {
            "price": price,
            "p10": p10f,
            "ratio": ratio,
            "gap": gap,
            "gap_frac": (gap / p10f) if p10f > 0 else 0.0,
            "held": held,
            "share": share,
            "fill": held / max(share, 1.0),
            "sales": sales,
            "buys": buys,
            "vel": sales - buys,
            "demand": sales / max(buys, 0.5),
            "book_n": float(obs.get("book_n") or 0),
            "depth_ok": 1.0 if depth in ("ok", "thick") else 0.0,
            "depth_thick": 1.0 if depth == "thick" else 0.0,
            "night": 1.0 if obs.get("night") else 0.0,
            "empty_streak": float(getattr(st, "empty_streak", 0) or 0),
            "step": step,
            "nac": float(nac),
            "try_sells": float(obs.get("try_sells") or 0),
            "seller_n": float(obs.get("seller_n") or 0),
            "up_cd": float(getattr(st, "up_cd", 0) or 0),
            "down_cd": float(getattr(st, "down_cd", 0) or 0),
            "up_streak": float(getattr(st, "up_streak", 0) or 0),
        }
    )


# ── AST ────────────────────────────────────────────────────────────────

# Expr: float
# Pred: bool
# ActionProg: returns ("UP"|"HOLD"|"DOWN", optional absolute target or None)


@dataclass
class Const:
    v: float

    def eval(self, f: Features) -> float:
        return self.v

    def nodes(self) -> int:
        return 1

    def consts(self) -> List["Const"]:
        return [self]

    def pretty(self) -> str:
        return f"{self.v:.4g}"


@dataclass
class Feat:
    name: str

    def eval(self, f: Features) -> float:
        return f.get(self.name)

    def nodes(self) -> int:
        return 1

    def consts(self) -> List[Const]:
        return []

    def pretty(self) -> str:
        return self.name


@dataclass
class BinOp:
    op: str  # + - * / max min
    a: Any
    b: Any

    def eval(self, f: Features) -> float:
        x, y = self.a.eval(f), self.b.eval(f)
        if self.op == "+":
            return x + y
        if self.op == "-":
            return x - y
        if self.op == "*":
            return x * y
        if self.op == "/":
            return x / y if abs(y) > 1e-9 else 0.0
        if self.op == "max":
            return max(x, y)
        if self.op == "min":
            return min(x, y)
        return x

    def nodes(self) -> int:
        return 1 + self.a.nodes() + self.b.nodes()

    def consts(self) -> List[Const]:
        return self.a.consts() + self.b.consts()

    def pretty(self) -> str:
        return f"({self.a.pretty()} {self.op} {self.b.pretty()})"


@dataclass
class Cmp:
    op: str  # > >= < <=
    a: Any
    b: Any

    def eval(self, f: Features) -> bool:
        x, y = self.a.eval(f), self.b.eval(f)
        if self.op == ">":
            return x > y
        if self.op == ">=":
            return x >= y
        if self.op == "<":
            return x < y
        if self.op == "<=":
            return x <= y
        return False

    def nodes(self) -> int:
        return 1 + self.a.nodes() + self.b.nodes()

    def consts(self) -> List[Const]:
        return self.a.consts() + self.b.consts()

    def pretty(self) -> str:
        return f"({self.a.pretty()} {self.op} {self.b.pretty()})"


@dataclass
class And:
    a: Any
    b: Any

    def eval(self, f: Features) -> bool:
        return bool(self.a.eval(f) and self.b.eval(f))

    def nodes(self) -> int:
        return 1 + self.a.nodes() + self.b.nodes()

    def consts(self) -> List[Const]:
        return self.a.consts() + self.b.consts()

    def pretty(self) -> str:
        return f"({self.a.pretty()} && {self.b.pretty()})"


@dataclass
class Or:
    a: Any
    b: Any

    def eval(self, f: Features) -> bool:
        return bool(self.a.eval(f) or self.b.eval(f))

    def nodes(self) -> int:
        return 1 + self.a.nodes() + self.b.nodes()

    def consts(self) -> List[Const]:
        return self.a.consts() + self.b.consts()

    def pretty(self) -> str:
        return f"({self.a.pretty()} || {self.b.pretty()})"


@dataclass
class Not:
    a: Any

    def eval(self, f: Features) -> bool:
        return not bool(self.a.eval(f))

    def nodes(self) -> int:
        return 1 + self.a.nodes()

    def consts(self) -> List[Const]:
        return self.a.consts()

    def pretty(self) -> str:
        return f"!{self.a.pretty()}"


@dataclass
class IfAction:
    """if pred then left else right — each branch is ActionLeaf or IfAction."""

    pred: Any
    then_: Any
    else_: Any

    def nodes(self) -> int:
        return 1 + self.pred.nodes() + self.then_.nodes() + self.else_.nodes()

    def consts(self) -> List[Const]:
        return self.pred.consts() + self.then_.consts() + self.else_.consts()

    def pretty(self) -> str:
        return f"if {self.pred.pretty()} then {self.then_.pretty()} else {self.else_.pretty()}"


@dataclass
class ActionLeaf:
    """Discrete action + optional target expression (absolute price)."""

    action: str  # UP HOLD DOWN
    target: Optional[Any] = None  # Expr → absolute price; None → step nudge
    step_mult: Const = field(default_factory=lambda: Const(1.0))

    def nodes(self) -> int:
        n = 1 + self.step_mult.nodes()
        if self.target is not None:
            n += self.target.nodes()
        return n

    def consts(self) -> List[Const]:
        out = list(self.step_mult.consts())
        if self.target is not None:
            out.extend(self.target.consts())
        return out

    def pretty(self) -> str:
        if self.target is not None:
            return f"{self.action}→{self.target.pretty()}"
        return f"{self.action}×{self.step_mult.pretty()}"


Program = Union[IfAction, ActionLeaf]


def eval_program(prog: Program, f: Features, price: float, step: float) -> Tuple[str, int]:
    node: Any = prog
    while isinstance(node, IfAction):
        node = node.then_ if node.pred.eval(f) else node.else_
    assert isinstance(node, ActionLeaf)
    act = node.action
    if act == "HOLD":
        return "HOLD", int(price)
    if node.target is not None:
        tgt = int(max(step, node.target.eval(f)))
        if act == "UP":
            return "UP", max(int(price + step), tgt)
        if act == "DOWN":
            return "DOWN", min(int(price - step), tgt) if tgt < price else int(max(step, price - step))
    delta = max(1, int(step * abs(node.step_mult.v)))
    if act == "UP":
        return "UP", int(price + delta)
    if act == "DOWN":
        return "DOWN", int(max(step, price - delta))
    return "HOLD", int(price)


# ── Hand seeds (interpretable baselines as programs) ───────────────────

def prog_hold() -> ActionLeaf:
    return ActionLeaf("HOLD")


def prog_market_follow(pull: float = 0.9) -> IfAction:
    """If |gap| > step: move toward p10 by pull*gap; else HOLD."""
    return IfAction(
        Cmp(">", BinOp("max", Feat("gap"), BinOp("*", Feat("gap"), Const(-1.0))), Feat("step")),
        IfAction(
            Cmp(">", Feat("gap"), Const(0.0)),
            ActionLeaf("UP", target=BinOp("+", Feat("price"), BinOp("*", Const(pull), Feat("gap")))),
            ActionLeaf("DOWN", target=BinOp("+", Feat("price"), BinOp("*", Const(pull), Feat("gap")))),
        ),
        ActionLeaf("HOLD"),
    )


def prog_corridor_simple() -> IfAction:
    """Classic: fill>hi → DOWN; fill<lo & sales>=3 → UP; else HOLD."""
    return IfAction(
        Cmp(">", Feat("fill"), Const(0.28)),
        ActionLeaf("DOWN", step_mult=Const(1.0)),
        IfAction(
            And(Cmp("<", Feat("fill"), Const(0.18)), Cmp(">=", Feat("sales"), Const(3.0))),
            ActionLeaf("UP", step_mult=Const(1.0)),
            ActionLeaf("HOLD"),
        ),
    )


def prog_under_veto_down() -> IfAction:
    """Never DOWN when ratio < 0.95; else corridor-ish."""
    return IfAction(
        And(Cmp(">", Feat("fill"), Const(0.28)), Cmp(">=", Feat("ratio"), Const(0.95))),
        ActionLeaf("DOWN", step_mult=Const(2.0)),
        IfAction(
            And(Cmp("<", Feat("fill"), Const(0.18)), Cmp(">=", Feat("sales"), Const(3.0))),
            ActionLeaf("UP", step_mult=Const(1.0)),
            ActionLeaf("HOLD"),
        ),
    )


# ── Random GP primitives ───────────────────────────────────────────────

def _rand_expr(rng: random.Random, depth: int) -> Any:
    if depth <= 0 or rng.random() < 0.45:
        if rng.random() < 0.55:
            return Feat(rng.choice(FEATURE_NAMES))
        return Const(round(rng.choice([0.0, 0.15, 0.18, 0.25, 0.28, 0.5, 0.85, 0.9, 0.95, 1.0, 1.1, 2.0, 3.0]) * rng.choice([1.0, 1.0, rng.uniform(0.5, 1.5)]), 4))
    op = rng.choice(["+", "-", "*", "/", "max", "min"])
    return BinOp(op, _rand_expr(rng, depth - 1), _rand_expr(rng, depth - 1))


def _rand_pred(rng: random.Random, depth: int) -> Any:
    if depth <= 0 or rng.random() < 0.5:
        return Cmp(rng.choice([">", ">=", "<", "<="]), _rand_expr(rng, depth), _rand_expr(rng, depth))
    kind = rng.choice(["and", "or", "not", "cmp"])
    if kind == "not":
        return Not(_rand_pred(rng, depth - 1))
    if kind == "and":
        return And(_rand_pred(rng, depth - 1), _rand_pred(rng, depth - 1))
    if kind == "or":
        return Or(_rand_pred(rng, depth - 1), _rand_pred(rng, depth - 1))
    return Cmp(rng.choice([">", "<"]), _rand_expr(rng, depth), _rand_expr(rng, depth))


def _rand_leaf(rng: random.Random) -> ActionLeaf:
    act = rng.choice(["UP", "HOLD", "DOWN", "HOLD", "UP", "DOWN"])
    if act == "HOLD":
        return ActionLeaf("HOLD")
    if rng.random() < 0.35:
        # target toward p10
        return ActionLeaf(act, target=BinOp("+", Feat("price"), BinOp("*", Const(round(rng.uniform(0.2, 1.0), 3)), Feat("gap"))))
    return ActionLeaf(act, step_mult=Const(round(rng.uniform(0.5, 4.0), 3)))


def sample_program(rng: random.Random, max_depth: int = 3) -> Program:
    if rng.random() < 0.2:
        return _rand_leaf(rng)
    return IfAction(_rand_pred(rng, max_depth), sample_program(rng, max_depth - 1) if max_depth > 0 else _rand_leaf(rng), sample_program(rng, max_depth - 1) if max_depth > 0 else _rand_leaf(rng))


def mutate_consts(prog: Program, rng: random.Random, sigma: float = 0.15) -> Program:
    """Inner-loop style: jitter Const leaves in place (shallow copy via shared mutation)."""
    import copy

    p = copy.deepcopy(prog)
    for c in p.consts():
        if rng.random() < 0.5:
            c.v = round(c.v * (1.0 + rng.uniform(-sigma, sigma)) + rng.uniform(-0.02, 0.02), 4)
    return p


def complexity(prog: Program) -> int:
    return prog.nodes()
