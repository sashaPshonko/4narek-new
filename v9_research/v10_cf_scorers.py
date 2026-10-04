#!/usr/bin/env python3
"""Load CF ↓/↑ scorers and build feature rows for v10 grey zone."""
from __future__ import annotations

from pathlib import Path

import joblib
import numpy as np

ROOT = Path(__file__).resolve().parent
DOWN_PATH = ROOT / "v10_down_hold_model" / "cf_model.joblib"
UP_PATH = ROOT / "v10_up_hold_model" / "cf_model.joblib"


def _hour_night(ts: str) -> int:
    try:
        hour = int(ts[11:13])
        return 1 if 0 <= hour < 6 else 0
    except Exception:
        return 0


def feats_down(held: int, obs: dict, price: int | None = None) -> dict:
    share = max(int(obs.get("share") or 12), 1)
    sales = float(obs.get("sales") or 0)
    buys = float(obs.get("buys") or 0)
    try_s = float(obs.get("try_sells") or 0)
    mkt = obs.get("mkt") or obs.get("p10")
    price = int(price if price is not None else obs.get("price_before") or 0)
    step = max(int(obs.get("step") or 1), 1)
    turn = sales / max(held, 1)
    return {
        "held": float(held),
        "share": float(share),
        "load": held / share,
        "sales": sales,
        "buys": buys,
        "try_sells": try_s,
        "turn": turn,
        "doi": held / max(sales, 0.25),
        "stock_load": float(obs.get("stock_load") or held / share),
        "price": float(price),
        "step": float(step),
        "night": float(_hour_night(obs.get("ts") or "")),
        "ratio": float((price / mkt) if mkt else 1.0),
    }


def feats_up(held: int, obs: dict, price: int | None = None) -> dict:
    fr = feats_down(held, obs, price=price)
    mkt = obs.get("mkt") or obs.get("p10")
    price = int(fr["price"])
    step = max(int(fr["step"]), 1)
    floor = int(0.75 * mkt) if mkt else max(step, price // 4)
    mid = int(1.05 * mkt) if mkt else price + 10 * step
    if mid <= floor:
        mid = floor + step
    fr["pos_synth"] = max(0.0, min(1.0, (price - floor) / (mid - floor)))
    return fr


class CFScorers:
    def __init__(
        self,
        down_path: Path = DOWN_PATH,
        up_path: Path = UP_PATH,
        down_tau: float | None = None,
        up_tau: float | None = None,
        down_tau_floor: float = 0.55,
    ):
        down_b = joblib.load(down_path)
        up_b = joblib.load(up_path)
        self.down_clf = down_b["clf"]
        self.up_clf = up_b["clf"]
        self.down_feats = list(down_b["feats"])
        self.up_feats = list(up_b["feats"])
        # calibrated tau, but ↓ not below floor (sim bias toward dump)
        cal_down = float(down_b.get("tau", 0.5))
        cal_up = float(up_b.get("tau", 0.5))
        self.down_tau = float(down_tau) if down_tau is not None else max(cal_down, down_tau_floor)
        self.up_tau = float(up_tau) if up_tau is not None else cal_up
        self.cal_down_tau = cal_down
        self.cal_up_tau = cal_up

    def _vec(self, fr: dict, names: list[str]) -> np.ndarray:
        x = np.array([[float(fr.get(n, 0.0)) for n in names]], dtype=np.float64)
        return np.nan_to_num(x, nan=0.0, posinf=1e6, neginf=0.0)

    def p_down(self, held: int, obs: dict, price: int | None = None) -> float:
        fr = feats_down(held, obs, price=price)
        return float(self.down_clf.predict_proba(self._vec(fr, self.down_feats))[0, 1])

    def p_up(self, held: int, obs: dict, price: int | None = None) -> float:
        fr = feats_up(held, obs, price=price)
        return float(self.up_clf.predict_proba(self._vec(fr, self.up_feats))[0, 1])
