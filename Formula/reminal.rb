class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.10.2"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.2/reminal_0.10.2_darwin_arm64.tar.gz"
      sha256 "f21971280c7f85e010283802da32a451c0158499fcfa0b8ddeb0816db56097b1"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.2/reminal_0.10.2_darwin_amd64.tar.gz"
      sha256 "8223a1df4a6376014811d2f321d891aee900d04de518e2d043a4748c0b8da99b"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.2/reminal_0.10.2_linux_arm64.tar.gz"
      sha256 "e4bde293cd53b538375fd618cb9ac55b37b9ac1defca5e4282cc56ac2790cb3b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.2/reminal_0.10.2_linux_amd64.tar.gz"
      sha256 "df4b6e66aa5b13ef43c09e5b840cf56a3db6d3ebe45b5f64210dba5c118e7dad"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
