class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.10.0"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.0/reminal_0.10.0_darwin_arm64.tar.gz"
      sha256 "84fe829fb202b81296ed85d4f35528a7bd6ce16076214e869d958b271a65f8d8"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.0/reminal_0.10.0_darwin_amd64.tar.gz"
      sha256 "2e483295ed6edc6d44a39f300f01b4b35832ed89c6e6670f2ddbab2e79c8c102"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.0/reminal_0.10.0_linux_arm64.tar.gz"
      sha256 "a822a2576a3bd22b0006504655c1c4642308ae6b2692da37601a61d33ec76e5e"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.0/reminal_0.10.0_linux_amd64.tar.gz"
      sha256 "71bf1de3614055965dbceb2db9c044162e476992c26b293f054eb7e556bc91b0"
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
