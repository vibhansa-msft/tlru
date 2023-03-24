# TLRU
Most conventional LRUs maintain a link list of nodes and when user want to expire a node
they take the last node fromt he list and evict it from the list. However there is no 
implmentation for time based expiry of nodes.
Even in algorithms where time based expiry is supported each node holds a time field
and it needs to be evaluated to see which one has expired.
In TLRU we support time based expiry without maintaining time in each individual element.

