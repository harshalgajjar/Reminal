class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.2.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.1/reminal_1.2.1_darwin_arm64.tar.gz"
      sha256 "b82465cf6b6a5c5ba5e57fd810e7956283f740589e54f672e778c54080d34a98"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.1/reminal_1.2.1_darwin_amd64.tar.gz"
      sha256 "eb09fdc79fdd90df7bd756d1219f8ab82e9d4fc88e6c44f72933ee2ff142c9e0"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.1/reminal_1.2.1_linux_arm64.tar.gz"
      sha256 "f36942a72009a28e5f18c6cf1d172531ec237ad5b1872c569714acbdef9db2d6"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.1/reminal_1.2.1_linux_amd64.tar.gz"
      sha256 "f108eb481eb4863a303915b464daed324ae98fc4d7201d0285214b90a8040d93"
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
