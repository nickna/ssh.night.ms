namespace Night.Ms.SshServer.Doors.Games.Holdem.Evaluator;

// Comparable score for any 5-of-7 evaluation. Category dominates; T1..T5 encode in-category
// tiebreakers (e.g. pair rank, then kicker ranks). Unused slots are 0. Ace high = 14;
// wheel straight (A-2-3-4-5) reports T1 = 5.
public readonly record struct HandValue(HandCategory Category, int T1, int T2, int T3, int T4, int T5)
    : IComparable<HandValue>
{
    public int CompareTo(HandValue other)
    {
        var c = Category.CompareTo(other.Category); if (c != 0) return c;
        c = T1.CompareTo(other.T1); if (c != 0) return c;
        c = T2.CompareTo(other.T2); if (c != 0) return c;
        c = T3.CompareTo(other.T3); if (c != 0) return c;
        c = T4.CompareTo(other.T4); if (c != 0) return c;
        return T5.CompareTo(other.T5);
    }

    public static bool operator <(HandValue a, HandValue b) => a.CompareTo(b) < 0;
    public static bool operator >(HandValue a, HandValue b) => a.CompareTo(b) > 0;
    public static bool operator <=(HandValue a, HandValue b) => a.CompareTo(b) <= 0;
    public static bool operator >=(HandValue a, HandValue b) => a.CompareTo(b) >= 0;
}
