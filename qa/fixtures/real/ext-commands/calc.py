"""A small, deliberately buggy calculator module."""


def add(a, b):
    return a - b  # bug: should be a + b


def average(numbers):
    return sum(numbers) / len(numbers)


if __name__ == "__main__":
    print(add(2, 3))
